package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/budget"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/ratelimit"
)

// --- test doubles -----------------------------------------------------------

type fakeForwarder struct {
	called  bool
	gotPath string
	gotBody []byte
	resp    *UpstreamResponse
	err     error
}

func (f *fakeForwarder) Forward(_ context.Context, path string, body []byte) (*UpstreamResponse, error) {
	f.called = true
	f.gotPath = path
	f.gotBody = append([]byte(nil), body...)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

type fakeLimiter struct{ decision ratelimit.Decision }

func (f fakeLimiter) Allow(uint, int) ratelimit.Decision { return f.decision }

type fakeBudget struct {
	status budget.Status
	err    error
}

func (f fakeBudget) Status(context.Context, apikey.APIKey) (budget.Status, error) {
	return f.status, f.err
}

// fakePool records what priority the handler asked for and how often the slot
// came back. Release is one-shot like the real queue.Release, so a test can
// assert the slot is returned exactly once however many times the handler calls
// it.
type fakePool struct {
	acquired int
	priority int
	released int
	err      error
}

func (p *fakePool) Acquire(_ context.Context, priority int) (queue.Release, error) {
	p.acquired++
	p.priority = priority
	if p.err != nil {
		return nil, p.err
	}
	var once sync.Once
	return func() { once.Do(func() { p.released++ }) }, nil
}

type fakeUsage struct {
	events []*usage.Event
	err    error
}

func (f *fakeUsage) Record(_ context.Context, e *usage.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

// --- helpers ----------------------------------------------------------------

func testKey() *apikey.APIKey {
	return &apikey.APIKey{
		ID: 1, Name: "test", BatchMax: 25, RatePerMin: 400,
		TokenBudget: apikey.Unlimited, BudgetPeriod: apikey.Lifetime,
	}
}

func okResponse(body string) *UpstreamResponse {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &UpstreamResponse{StatusCode: http.StatusOK, Header: h, Body: []byte(body)}
}

// deps bundles the pluggable doubles with sensible allow-everything defaults.
type deps struct {
	fw   *fakeForwarder
	rl   rateLimiter
	bc   budgetChecker
	ur   *fakeUsage
	pool *fakePool
}

func defaultDeps() deps {
	return deps{
		fw:   &fakeForwarder{resp: okResponse(`{"usage":{"prompt_tokens":42}}`)},
		rl:   fakeLimiter{decision: ratelimit.Decision{Allowed: true}},
		bc:   fakeBudget{status: budget.Status{Exhausted: false, Remaining: apikey.Unlimited}},
		ur:   &fakeUsage{},
		pool: &fakePool{},
	}
}

func (d deps) handler() *Handler {
	return NewHandler(d.bc, d.rl, d.ur, d.fw, d.pool, "bge-m3",
		func() time.Time { return time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC) },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func serve(t *testing.T, h *Handler, body string, key *apikey.APIKey) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	if key != nil {
		r = r.WithContext(WithAPIKey(r.Context(), key))
	}
	w := httptest.NewRecorder()
	h.Embeddings(w, r)
	return w
}

// --- tests ------------------------------------------------------------------

func TestSuccessRelaysAndRecords(t *testing.T) {
	d := defaultDeps()
	body := `{"model":"bge-m3","input":["a","b","c"]}`
	w := serve(t, d.handler(), body, testKey())

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"usage":{"prompt_tokens":42}}`, w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Forwarded verbatim to the same path.
	require.True(t, d.fw.called)
	require.Equal(t, "/v1/embeddings", d.fw.gotPath)
	require.Equal(t, body, string(d.fw.gotBody))

	// Usage recorded from the upstream count, with the batch size we measured.
	require.Len(t, d.ur.events, 1)
	require.Equal(t, int64(42), d.ur.events[0].PromptTokens)
	require.Equal(t, 3, d.ur.events[0].BatchSize)
	require.Equal(t, "bge-m3", d.ur.events[0].Model)
	require.Equal(t, uint(1), d.ur.events[0].APIKeyID)
}

func TestBudgetRemainingHeaderOnLimitedKey(t *testing.T) {
	d := defaultDeps()
	d.bc = fakeBudget{status: budget.Status{Exhausted: false, Remaining: 500}}
	w := serve(t, d.handler(), `{"input":"hello"}`, testKey())

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "500", w.Header().Get("X-Token-Budget-Remaining"))
}

func TestMissingKeyIsUnauthorized(t *testing.T) {
	d := defaultDeps()
	w := serve(t, d.handler(), `{"input":"hi"}`, nil) // no key in context

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.False(t, d.fw.called)
}

func TestBadJSONIsRejected(t *testing.T) {
	d := defaultDeps()
	w := serve(t, d.handler(), `{"input": `, testKey())

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.False(t, d.fw.called)
}

func TestBatchTooLargeIsRejectedBeforeForward(t *testing.T) {
	d := defaultDeps()
	key := testKey()
	key.BatchMax = 2
	w := serve(t, d.handler(), `{"input":["a","b","c"]}`, key)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "exceeds the limit of 2")
	require.False(t, d.fw.called, "an over-limit batch must not reach the upstream")
}

// TestMixedArrayCannotBypassBatchLimit is the regression test for the Codex
// finding: a leading number must not make an oversized mixed array count as one.
func TestMixedArrayCannotBypassBatchLimit(t *testing.T) {
	d := defaultDeps()
	key := testKey()
	key.BatchMax = 2
	w := serve(t, d.handler(), `{"input":[0,"a","b","c"]}`, key)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.False(t, d.fw.called, "the oversized mixed batch must not reach the upstream")
}

func TestEmptyInputArrayIsRejected(t *testing.T) {
	d := defaultDeps()
	w := serve(t, d.handler(), `{"input":[]}`, testKey())

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.False(t, d.fw.called)
}

func TestRateLimitedReturns429WithRetryAfter(t *testing.T) {
	d := defaultDeps()
	d.rl = fakeLimiter{decision: ratelimit.Decision{Allowed: false, RetryAfter: 37 * time.Second}}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "37", w.Header().Get("Retry-After"))
	require.False(t, d.fw.called)
}

func TestBudgetExhaustedReturns402(t *testing.T) {
	d := defaultDeps()
	d.bc = fakeBudget{status: budget.Status{Exhausted: true, Remaining: 0}}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusPaymentRequired, w.Code)
	require.False(t, d.fw.called)
}

func TestUpstreamErrorReturns502(t *testing.T) {
	d := defaultDeps()
	d.fw = &fakeForwarder{err: io.ErrUnexpectedEOF}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, d.ur.events)
}

// TestSingleStringAndTokenArrayCountAsOne pins OpenAI input semantics: a bare
// string and an array of token ids are each a single input, so a batch-1 key
// accepts both.
func TestSingleStringAndTokenArrayCountAsOne(t *testing.T) {
	for _, body := range []string{`{"input":"hello"}`, `{"input":[1,2,3,4]}`} {
		d := defaultDeps()
		key := testKey()
		key.BatchMax = 1
		w := serve(t, d.handler(), body, key)
		require.Equalf(t, http.StatusOK, w.Code, "body %s should be one input", body)
		require.True(t, d.fw.called)
	}
}

// TestNoUsageRecordedWhenUpstreamRejects: a non-200 from Ollama is relayed as-is
// and must not be billed against the key.
func TestNoUsageRecordedWhenUpstreamRejects(t *testing.T) {
	d := defaultDeps()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	d.fw = &fakeForwarder{resp: &UpstreamResponse{
		StatusCode: http.StatusBadRequest,
		Header:     h,
		Body:       []byte(`{"error":"model not found"}`),
	}}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.JSONEq(t, `{"error":"model not found"}`, w.Body.String())
	require.Empty(t, d.ur.events, "a failed upstream call must not be billed")
}

// TestSlotIsTakenAtTheKeysPriorityAndGivenBack pins the admission contract: the
// key's rank decides its place in the queue, and the slot is returned exactly
// once so capacity is never leaked by a served request.
func TestSlotIsTakenAtTheKeysPriorityAndGivenBack(t *testing.T) {
	d := defaultDeps()
	key := testKey()
	key.Priority = apikey.MaxPriority
	w := serve(t, d.handler(), `{"input":"hi"}`, key)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, d.pool.acquired)
	require.Equal(t, apikey.MaxPriority, d.pool.priority)
	require.Equal(t, 1, d.pool.released)
}

// TestSlotIsGivenBackWhenUpstreamFails: a dead backend must not permanently
// shrink the pool's capacity.
func TestSlotIsGivenBackWhenUpstreamFails(t *testing.T) {
	d := defaultDeps()
	d.fw = &fakeForwarder{err: io.ErrUnexpectedEOF}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, 1, d.pool.released)
}

// TestNoSlotIsTakenWhenALocalCheckRejects: the cheap checks run first precisely
// so a rejected request never occupies a queue slot.
func TestNoSlotIsTakenWhenALocalCheckRejects(t *testing.T) {
	cases := map[string]func(*deps, *apikey.APIKey){
		"rate limited": func(d *deps, _ *apikey.APIKey) {
			d.rl = fakeLimiter{decision: ratelimit.Decision{Allowed: false, RetryAfter: time.Second}}
		},
		"budget exhausted": func(d *deps, _ *apikey.APIKey) {
			d.bc = fakeBudget{status: budget.Status{Exhausted: true}}
		},
		"batch too large": func(_ *deps, k *apikey.APIKey) { k.BatchMax = 1 },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d := defaultDeps()
			key := testKey()
			setup(&d, key)
			serve(t, d.handler(), `{"input":["a","b","c"]}`, key)

			require.Zero(t, d.pool.acquired, "a rejected request must not queue for the pool")
			require.False(t, d.fw.called)
		})
	}
}

// TestAbandonedQueuedRequestReturns503: when the client hangs up (or the gateway
// shuts down) while queued, no slot is held and nothing is forwarded or billed.
func TestAbandonedQueuedRequestReturns503(t *testing.T) {
	d := defaultDeps()
	d.pool = &fakePool{err: context.Canceled}
	w := serve(t, d.handler(), `{"input":"hi"}`, testKey())

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "queued")
	require.False(t, d.fw.called)
	require.Zero(t, d.pool.released, "no slot was held, so none is returned")
	require.Empty(t, d.ur.events)
}
