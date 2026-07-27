package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/budget"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/ratelimit"
)

// defaultMaxBodyBytes caps the request body. A batch of 25 inputs at bge-m3's
// ~8k-token ceiling is well under this; the limit exists to stop a hostile
// client from streaming an unbounded body into memory before validation.
const defaultMaxBodyBytes int64 = 10 << 20 // 10 MiB

// The narrow interfaces below are declared at the consumer so the handler
// depends only on the behaviour it uses and can be driven by fakes in tests.

// budgetChecker reports whether a key still has allowance.
type budgetChecker interface {
	Status(ctx context.Context, k apikey.APIKey) (budget.Status, error)
}

// rateLimiter records a request and reports whether it is within the key's rate.
type rateLimiter interface {
	Allow(keyID uint, limit int) ratelimit.Decision
}

// usageRecorder persists a usage event after a successful upstream call.
type usageRecorder interface {
	Record(ctx context.Context, e *usage.Event) error
}

// Handler serves POST /v1/embeddings. It is the enforcement point: it runs the
// cheap, local checks before spending an upstream call, forwards accepted
// requests to Caddy, and records the bge-m3 tokens Ollama reports.
type Handler struct {
	budget    budgetChecker
	limiter   rateLimiter
	usage     usageRecorder
	forwarder Forwarder
	model     string
	maxBody   int64
	now       func() time.Time
	logger    *slog.Logger
}

// NewHandler wires the enforcement pipeline. model is recorded with usage (bge-m3);
// now is injectable for deterministic tests; logger may be nil (defaults to slog).
func NewHandler(
	bc budgetChecker,
	rl rateLimiter,
	ur usageRecorder,
	fw Forwarder,
	model string,
	now func() time.Time,
	logger *slog.Logger,
) *Handler {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		budget:    bc,
		limiter:   rl,
		usage:     ur,
		forwarder: fw,
		model:     model,
		maxBody:   defaultMaxBodyBytes,
		now:       now,
		logger:    logger,
	}
}

// Embeddings handles one embeddings request. The check order is deliberate —
// authentication, then decode, then the cheapest domain checks — so malformed or
// abusive traffic is rejected before it can cost an upstream call. Every failure
// path returns an OpenAI-style error with the appropriate status:
//
//	401 missing/invalid key   400 bad body / batch too large
//	429 rate limited (+Retry-After)   402 budget exhausted   502 upstream down
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	// The auth middleware must have attached the key; its absence is treated as
	// unauthenticated rather than trusted.
	key, ok := APIKeyFrom(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "invalid_request_error", "missing API key")
		return
	}

	// Bound the body before reading it all into memory.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader signals an over-limit body via its error on read.
		WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
		return
	}

	var req embeddingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "request body is not valid JSON")
		return
	}

	// Pass the key's batch limit so counting can stop early on an oversized batch.
	n, err := countInputs(req.Input, key.BatchMax)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if !key.AllowsBatch(n) {
		WriteError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("batch size %d exceeds the limit of %d inputs per request", n, key.BatchMax))
		return
	}

	// Rate limit before the budget query so a flood is shed without hitting the DB.
	if d := h.limiter.Allow(key.ID, key.RatePerMin); !d.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(d.RetryAfterSeconds()))
		WriteError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			fmt.Sprintf("rate limit of %d requests/min exceeded; retry in %d seconds", key.RatePerMin, d.RetryAfterSeconds()))
		return
	}

	st, err := h.budget.Status(r.Context(), *key)
	if err != nil {
		h.logger.Error("budget check failed", "key_id", key.ID, "err", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not evaluate token budget")
		return
	}
	if st.Exhausted {
		// 402 (not 429): the client cannot wait out a prepaid-quota exhaustion the
		// way it can wait out a per-minute rate limit.
		WriteError(w, http.StatusPaymentRequired, "insufficient_quota",
			"token budget exhausted for this key")
		return
	}

	// Forward the original body verbatim so no client field is lost or reshaped.
	up, err := h.forwarder.Forward(r.Context(), r.URL.Path, body)
	if err != nil {
		h.logger.Error("upstream forward failed", "key_id", key.ID, "err", err)
		WriteError(w, http.StatusBadGateway, "upstream_error", "embedding backend unavailable")
		return
	}

	// Account only for successful calls, using Ollama's own token count. Recording
	// is best-effort: a bookkeeping failure must not fail the client's request.
	if up.StatusCode == http.StatusOK {
		tokens := parsePromptTokens(up.Body)
		ev := &usage.Event{
			APIKeyID:     key.ID,
			Model:        h.model,
			PromptTokens: tokens,
			BatchSize:    n,
			CreatedAt:    h.now(),
		}
		if err := h.usage.Record(r.Context(), ev); err != nil {
			h.logger.Error("usage record failed", "key_id", key.ID, "tokens", tokens, "err", err)
		}
		// Surface remaining budget to well-behaved clients (omitted for unlimited).
		if st.Remaining != apikey.Unlimited {
			w.Header().Set("X-Token-Budget-Remaining", strconv.FormatInt(st.Remaining, 10))
		}
	}

	// Relay the upstream response transparently: preserve its content type and
	// status, and stream its body back unchanged.
	if ct := up.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(up.StatusCode)
	if _, err := w.Write(up.Body); err != nil {
		h.logger.Warn("writing response to client failed", "key_id", key.ID, "err", err)
	}
}
