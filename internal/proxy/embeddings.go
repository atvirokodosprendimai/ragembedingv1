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
//   - an array of strings (or an array of arrays) is one input per element;
//   - an array of numbers is a single pre-tokenized input, i.e. one input.
//
// It inspects the raw JSON structurally rather than unmarshalling into typed
// slices, so a hostile or malformed body cannot cause a large intermediate
// allocation before the batch limit is even checked.
func countInputs(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errInvalidInput
	}

	switch trimmed[0] {
	case '"':
		// A single quoted string. Validate it parses so we reject malformed JSON.
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return 0, errInvalidInput
		}
		return 1, nil
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return 0, errInvalidInput
		}
		if len(arr) == 0 {
			// An empty array is zero inputs; AllowsBatch(0) then rejects it as a
			// 400 with the batch message, which is the clearest signal to a client.
			return 0, nil
		}
		// If the first element is a JSON number, the array is one pre-tokenized
		// input rather than a batch of many.
		first := bytes.TrimSpace(arr[0])
		if len(first) > 0 && (first[0] == '-' || (first[0] >= '0' && first[0] <= '9')) {
			return 1, nil
		}
		return len(arr), nil
	default:
		return 0, errInvalidInput
	}
}

// upstreamUsage mirrors the usage block of Ollama's OpenAI-compatible embeddings
// response. prompt_tokens is the authoritative bge-m3 input-token count.
type upstreamUsage struct {
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

// parsePromptTokens pulls prompt_tokens out of an upstream response body. A body
// that lacks the field (or isn't the JSON we expect) yields 0 rather than an
// error: usage accounting is best-effort and must never fail a client's request.
func parsePromptTokens(body []byte) int64 {
	var u upstreamUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return 0
	}
	return u.Usage.PromptTokens
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

// writeAPIError writes an OpenAI-style JSON error with the given status.
func writeAPIError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding a fixed, small struct cannot fail in a way we can act on here; the
	// status line is already written.
	_ = json.NewEncoder(w).Encode(apiError{Error: apiErrorBody{Message: message, Type: errType}})
}
