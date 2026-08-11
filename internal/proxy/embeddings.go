package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

// errInvalidInput marks a request body whose "input" field is missing or not a
// string/array — a client error, not a server fault.
var errInvalidInput = errors.New("input must be a non-empty string or array")

// errInputTooLarge marks an input array with more elements than the gateway will
// scan. It bounds the work countInputs does on a hostile body: a single
// pre-tokenized input never legitimately exceeds a model's context (~8k tokens
// for bge-m3), so an array longer than this cap is refused outright rather than
// scanned in full.
var errInputTooLarge = errors.New("input array is too large")

// maxInputArrayElements caps how many top-level array elements countInputs will
// examine. It is far above any real batch or single pre-tokenized input, so it
// only ever trips on abuse.
const maxInputArrayElements = 100_000

// embeddingsRequest captures just the field the gateway needs to police: the
// polymorphic "input". Everything else in the body is forwarded to Ollama
// untouched, so we deliberately do not model it (and cannot accidentally drop or
// reshape a field the client sent).
type embeddingsRequest struct {
	Input json.RawMessage `json:"input"`
}

// countInputs returns how many distinct inputs a /v1/embeddings request carries,
// following OpenAI's input semantics:
//
//   - a JSON string is one input;
//   - an array whose elements are ALL numbers is a single pre-tokenized input;
//   - any other array is one input per top-level element (strings, or nested
//     token arrays).
//
// It streams the JSON with a token decoder rather than unmarshalling into typed
// slices, so a hostile body cannot force a large intermediate allocation before
// the batch limit is checked. limit is the key's BatchMax: once a non-numeric
// array has clearly exceeded it, counting stops early. The "all numbers" rule is
// only applied after seeing EVERY element — a mixed array like [0,"a","b"] is a
// 3-input batch, not one, which closes a batch-limit bypass.
func countInputs(raw json.RawMessage, limit int) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber() // numeric elements arrive as json.Number, not float64

	tok, err := dec.Token()
	if err != nil {
		return 0, errInvalidInput
	}

	switch t := tok.(type) {
	case string:
		return 1, nil // a single quoted string
	case json.Delim:
		if t != '[' {
			return 0, errInvalidInput // an object or other structure is not valid input
		}
		return countArrayInputs(dec, limit)
	default:
		return 0, errInvalidInput // a bare number, bool, or null is not valid input
	}
}

// countArrayInputs walks a top-level input array element by element. It returns
// 1 when every element is a number (a single pre-tokenized input) and the
// element count otherwise. Work is bounded by maxInputArrayElements and by an
// early exit once a non-numeric batch has passed the limit.
func countArrayInputs(dec *json.Decoder, limit int) (int, error) {
	count := 0
	allNumbers := true

	for dec.More() {
		el, err := dec.Token()
		if err != nil {
			return 0, errInvalidInput
		}
		switch v := el.(type) {
		case json.Delim:
			// A nested array/object element (e.g. a token list) — not a number,
			// and its contents must be consumed so decoding stays aligned.
			allNumbers = false
			if err := skipRest(dec, v); err != nil {
				return 0, errInvalidInput
			}
		case json.Number:
			// numeric element; array may still be a single pre-tokenized input
		default:
			// string, bool, or null element
			allNumbers = false
		}

		count++
		if count > maxInputArrayElements {
			return 0, errInputTooLarge
		}
		// Once the array can't collapse to a single input and already exceeds the
		// batch limit, the exact count no longer matters — reject early.
		if !allNumbers && count > limit {
			return count, nil
		}
	}

	if count == 0 {
		// An empty array is zero inputs; AllowsBatch(0) then rejects it with the
		// batch message, the clearest signal to a client.
		return 0, nil
	}
	if allNumbers {
		return 1, nil
	}
	return count, nil
}

// skipRest consumes the remaining tokens of a nested value whose opening delim
// (open) has already been read, tracking bracket depth until it closes.
func skipRest(dec *json.Decoder, open json.Delim) error {
	depth := 1
	_ = open // depth starts at 1 for the already-consumed opening delim
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
	}
	return nil
}

// upstreamUsage mirrors however the upstream reports input tokens. The two
// embedding APIs name the same number differently:
//
//	/v1/embeddings (OpenAI-compatible) -> {"usage":{"prompt_tokens":7}}
//	/api/embed     (Ollama native)     -> {"prompt_eval_count":7}
//
// Both are the authoritative bge-m3 input-token count for the request, so both
// are decoded and billed identically.
type upstreamUsage struct {
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
	PromptEvalCount int64 `json:"prompt_eval_count"`
}

// parsePromptTokens pulls the input-token count out of an upstream response
// body, accepting either API's spelling. A body that lacks both fields (or isn't
// the JSON we expect) yields 0 rather than an error: usage accounting is
// best-effort and must never fail a client's request.
func parsePromptTokens(body []byte) int64 {
	var u upstreamUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return 0
	}
	if u.Usage.PromptTokens > 0 {
		return u.Usage.PromptTokens
	}
	return u.PromptEvalCount
}

// apiError is the OpenAI-style error envelope. Emitting this shape means existing
// OpenAI SDK clients surface the gateway's errors through their normal error
// handling instead of choking on an unfamiliar body.
type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// WriteError writes an OpenAI-style JSON error with the given status. It is
// exported so the auth middleware emits the exact same error envelope as the
// handler, keeping the gateway's error surface consistent for SDK clients.
func WriteError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding a fixed, small struct cannot fail in a way we can act on here; the
	// status line is already written.
	_ = json.NewEncoder(w).Encode(apiError{Error: apiErrorBody{Message: message, Type: errType}})
}
