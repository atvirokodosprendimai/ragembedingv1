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
			updateCommand(),
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
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "human label for the key"},
		}, limitFlags()...),
		Action: func(ctx context.Context, c *cli.Command) error {
			st, err := openStores()
			if err != nil {
				return err
			}

			// Start from the configured defaults and let the flags override.
			limits := applyFlags(c, apikey.Limits{
				BatchMax:     st.cfg.Defaults.BatchMax,
				RatePerMin:   st.cfg.Defaults.RatePerMin,
				TokenBudget:  st.cfg.Defaults.TokenBudget,
				BudgetPeriod: apikey.BudgetPeriod(st.cfg.Defaults.BudgetPeriod),
				Priority:     st.cfg.Defaults.Priority,
			})
			// Validated by the domain, in the same place `update` validates, so
			// the two commands can never disagree about what is allowed.
			if err := limits.Validate(); err != nil {
				return err
			}

			plaintext, err := apikey.GenerateKey()
			if err != nil {
				return fmt.Errorf("generating key: %w", err)
			}
			k := &apikey.APIKey{
				Name:    c.String("name"),
				KeyHash: apikey.HashKey(plaintext),
			}
			if err := k.ApplyLimits(limits); err != nil {
				return err
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
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tPRIO\tBATCH\tRATE/min\tBUDGET\tPERIOD\tTOKENS(month)\tTOKENS(life)")
			for _, k := range keys {
				month, err := st.usage.SumTokens(ctx, k.ID, windows.ThisMonth.From, now)
				if err != nil {
					return err
				}
				life, err := st.usage.SumTokens(ctx, k.ID, time.Time{}, now)
				if err != nil {
					return err
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
					k.ID, dash(k.Name), status(k), priorityString(k.Priority),
					k.BatchMax, k.RatePerMin,
					budgetString(k.TokenBudget), k.BudgetPeriod,
					humanize.Comma(month), humanize.Comma(life))
			}
			return tw.Flush()
		},
	}
}

// limitFlags are the tunable parameters, shared by create and update so the two
// commands always speak the same language. Only the flags an operator actually
// passes take effect; the rest keep the value they already had.
func limitFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: "batch", Usage: "max inputs per request"},
		&cli.IntFlag{Name: "rate", Usage: "max requests per minute"},
		&cli.Int64Flag{Name: "budget", Usage: "token budget: -1 for unlimited, or a prepaid count (e.g. 100000000)"},
		&cli.StringFlag{Name: "period", Usage: "budget period: monthly|lifetime"},
		&cli.IntFlag{Name: "priority", Usage: "admission-queue rank: 0 = free/normal, up to 9 for front-of-house traffic"},
	}
}

// applyFlags overlays whatever the operator passed onto a starting set of
// limits. c.IsSet is what makes "leave the rest alone" work: an unset flag reads
// as its zero value, which for a rate limit would mean "reject everything".
func applyFlags(c *cli.Command, l apikey.Limits) apikey.Limits {
	if c.IsSet("batch") {
		l.BatchMax = c.Int("batch")
	}
	if c.IsSet("rate") {
		l.RatePerMin = c.Int("rate")
	}
	if c.IsSet("budget") {
		l.TokenBudget = c.Int64("budget")
	}
	if c.IsSet("period") {
		l.BudgetPeriod = apikey.BudgetPeriod(c.String("period"))
	}
	if c.IsSet("priority") {
		l.Priority = c.Int("priority")
	}
	return l
}

// updateCommand retunes an already-issued key. It exists so limits can be raised
// or lowered in place — the alternative is minting a replacement key and
// redeploying it everywhere it is configured, which is a lot of risk for a
// changed number.
func updateCommand() *cli.Command {
	return &cli.Command{
		Name:  "update",
		Usage: "change an existing key's limits (only the flags you pass are changed)",
		Flags: append([]cli.Flag{
			&cli.UintFlag{Name: "id", Usage: "id of the key to update", Required: true},
		}, limitFlags()...),
		Action: func(ctx context.Context, c *cli.Command) error {
			st, err := openStores()
			if err != nil {
				return err
			}

			// Read first: the operator sees what actually changed, and a mistyped
			// id fails before anything is written.
			id := c.Uint("id")
			k, err := st.keys.ByID(ctx, id)
			if err != nil {
				return err
			}

			before := k.Limits()
			after := applyFlags(c, before)
			if after == before {
				return fmt.Errorf("nothing to change: pass at least one of --batch, --rate, --budget, --period, --priority")
			}
			if err := after.Validate(); err != nil {
				return err
			}
			if err := st.keys.UpdateLimits(ctx, id, after); err != nil {
				return err
			}

			fmt.Printf("key %d (%s) updated:\n", id, dash(k.Name))
			printChanges(before, after)
			return nil
		},
	}
}

// printChanges reports only the fields that moved. Printing the untouched ones
// too would bury the change an operator is trying to confirm.
func printChanges(before, after apikey.Limits) {
	if before.BatchMax != after.BatchMax {
		fmt.Printf("  batch:    %d → %d inputs/request\n", before.BatchMax, after.BatchMax)
	}
	if before.RatePerMin != after.RatePerMin {
		fmt.Printf("  rate:     %d → %d requests/min\n", before.RatePerMin, after.RatePerMin)
	}
	if before.TokenBudget != after.TokenBudget {
		fmt.Printf("  budget:   %s → %s tokens\n", budgetString(before.TokenBudget), budgetString(after.TokenBudget))
	}
	if before.BudgetPeriod != after.BudgetPeriod {
		fmt.Printf("  period:   %s → %s\n", before.BudgetPeriod, after.BudgetPeriod)
	}
	if before.Priority != after.Priority {
		fmt.Printf("  priority: %s → %s\n", priorityString(before.Priority), priorityString(after.Priority))
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
	fmt.Printf("  prio:    %s\n", priorityString(k.Priority))
}

// priorityString renders a queue rank compactly enough for a table column, with
// an arrow on anything above the free tier so a promoted key is obvious at a
// glance rather than hiding in a column of similar digits.
func priorityString(p int) string {
	if p == apikey.NormalPriority {
		return "0"
	}
	return fmt.Sprintf("%d ↑", p)
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
