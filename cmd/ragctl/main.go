// Command ragctl is the operator CLI for issuing and managing API keys. Token
// creation is intentionally CLI-only (no self-service signup): an operator mints
// a key, sets its limits and budget, and hands the plaintext to the client once.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/config"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/platform/database"
)

func main() {
	cmd := &cli.Command{
		Name:  "ragctl",
		Usage: "issue and manage ragembedingv1 API keys",
		Commands: []*cli.Command{
			createCommand(),
			listCommand(),
			revokeCommand(),
		},
	}
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "ragctl:", err)
		os.Exit(1)
	}
}

// stores bundles the config and repositories every command needs. Opening it
// also runs migrations, so `ragctl create` works against a fresh database before
// the gateway has ever started.
type stores struct {
	cfg   config.Config
	keys  *database.APIKeyRepo
	usage *database.UsageRepo
}

func openStores() (*stores, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(db); err != nil {
		return nil, err
	}
	return &stores{
		cfg:   cfg,
		keys:  database.NewAPIKeyRepo(db),
		usage: database.NewUsageRepo(db),
	}, nil
}

// createCommand mints a key. Unset flags fall back to the .env defaults, so the
// common case (`ragctl create --name acme`) needs no tuning.
func createCommand() *cli.Command {
	return &cli.Command{
		Name:  "create",
		Usage: "create a new API key (limits default from .env; the key is shown once)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "human label for the key"},
			&cli.IntFlag{Name: "batch", Usage: "max inputs per request"},
			&cli.IntFlag{Name: "rate", Usage: "max requests per minute"},
			&cli.Int64Flag{Name: "budget", Usage: "token budget: -1 for unlimited, or a prepaid count (e.g. 100000000)"},
			&cli.StringFlag{Name: "period", Usage: "budget period: monthly|lifetime"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			st, err := openStores()
			if err != nil {
				return err
			}

			// Flag value if the operator set it, else the configured default.
			batch := st.cfg.Defaults.BatchMax
			if c.IsSet("batch") {
				batch = c.Int("batch")
			}
			rate := st.cfg.Defaults.RatePerMin
			if c.IsSet("rate") {
				rate = c.Int("rate")
			}
			tokenBudget := st.cfg.Defaults.TokenBudget
			if c.IsSet("budget") {
				tokenBudget = c.Int64("budget")
			}
			period := apikey.BudgetPeriod(st.cfg.Defaults.BudgetPeriod)
			if c.IsSet("period") {
				period = apikey.BudgetPeriod(c.String("period"))
			}

			// Validate before touching the database so a bad flag fails cleanly.
			if !period.Valid() {
				return fmt.Errorf("invalid --period %q (want monthly or lifetime)", period)
			}
			if batch < 1 {
				return fmt.Errorf("--batch must be >= 1, got %d", batch)
			}
			if rate < 1 {
				return fmt.Errorf("--rate must be >= 1, got %d", rate)
			}
			if tokenBudget < apikey.Unlimited || tokenBudget == 0 {
				return fmt.Errorf("--budget must be -1 (unlimited) or > 0, got %d", tokenBudget)
			}

			plaintext, err := apikey.GenerateKey()
			if err != nil {
				return fmt.Errorf("generating key: %w", err)
			}
			k := &apikey.APIKey{
				Name:         c.String("name"),
				KeyHash:      apikey.HashKey(plaintext),
				BatchMax:     batch,
				RatePerMin:   rate,
				TokenBudget:  tokenBudget,
				BudgetPeriod: period,
			}
			if err := st.keys.Create(ctx, k); err != nil {
				return err
			}

			printCreated(k, plaintext)
			return nil
		},
	}
}

// listCommand prints every key with its limits and token usage (this month and
// lifetime), so an operator can see at a glance who is spending what.
func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "list all API keys with their limits and usage",
		Action: func(ctx context.Context, _ *cli.Command) error {
			st, err := openStores()
			if err != nil {
				return err
			}
			keys, err := st.keys.List(ctx)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Println("no API keys yet — create one with: ragctl create --name <label>")
				return nil
			}

			now := time.Now()
			windows := usage.ReportWindows(now)

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tBATCH\tRATE/min\tBUDGET\tPERIOD\tTOKENS(month)\tTOKENS(life)")
			for _, k := range keys {
				month, err := st.usage.SumTokens(ctx, k.ID, windows.ThisMonth.From, now)
				if err != nil {
					return err
				}
				life, err := st.usage.SumTokens(ctx, k.ID, time.Time{}, now)
				if err != nil {
					return err
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
					k.ID, dash(k.Name), status(k), k.BatchMax, k.RatePerMin,
					budgetString(k.TokenBudget), k.BudgetPeriod,
					humanize.Comma(month), humanize.Comma(life))
			}
			return tw.Flush()
		},
	}
}

// revokeCommand disables a key by id. Revocation is idempotent.
func revokeCommand() *cli.Command {
	return &cli.Command{
		Name:  "revoke",
		Usage: "revoke an API key by id",
		Flags: []cli.Flag{
			&cli.UintFlag{Name: "id", Usage: "id of the key to revoke", Required: true},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			st, err := openStores()
			if err != nil {
				return err
			}
			id := c.Uint("id")
			if err := st.keys.Revoke(ctx, id); err != nil {
				return err
			}
			fmt.Printf("key %d revoked\n", id)
			return nil
		},
	}
}

// printCreated shows the one-time plaintext key with a clear warning; only its
// hash is stored, so it can never be recovered after this.
func printCreated(k *apikey.APIKey, plaintext string) {
	fmt.Println("API key created — copy it now, it will NOT be shown again:")
	fmt.Println()
	fmt.Printf("    %s\n", plaintext)
	fmt.Println()
	fmt.Printf("  id:      %d\n", k.ID)
	fmt.Printf("  name:    %s\n", dash(k.Name))
	fmt.Printf("  batch:   %d inputs/request\n", k.BatchMax)
	fmt.Printf("  rate:    %d requests/min\n", k.RatePerMin)
	fmt.Printf("  budget:  %s (%s)\n", budgetString(k.TokenBudget), k.BudgetPeriod)
}

// budgetString renders -1 as "unlimited" and any prepaid budget with thousands
// separators for readability.
func budgetString(budget int64) string {
	if budget == apikey.Unlimited {
		return "unlimited"
	}
	return humanize.Comma(budget)
}

func status(k apikey.APIKey) string {
	if k.IsRevoked() {
		return "revoked"
	}
	return "active"
}

// dash renders an empty label as a placeholder so table columns stay aligned.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
