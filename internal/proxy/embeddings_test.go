package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCountInputs covers the OpenAI input-shape semantics and, critically, the
// batch-limit bypass Codex flagged: a mixed array must count every element, not
// collapse to one because its first element is a number.
func TestCountInputs(t *testing.T) {
	const limit = 25
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "single string", input: `"hello"`, want: 1},
		{name: "array of strings", input: `["a","b","c"]`, want: 3},
		{name: "array of numbers is one pre-tokenized input", input: `[1,2,3,4,5]`, want: 1},
		{name: "array of token arrays", input: `[[1,2],[3,4],[5,6]]`, want: 3},
		{name: "mixed number-then-strings counts all (bypass fix)", input: `[0,"a","b","c"]`, want: 4},
		{name: "mixed strings-then-number counts all", input: `["a","b",0]`, want: 3},
		{name: "large all-number array under cap is one input", input: "[" + strings.Repeat("1,", 999) + "1]", want: 1},
		{name: "empty array", input: `[]`, want: 0},
		{name: "object is invalid", input: `{"x":1}`, wantErr: true},
		{name: "bare number is invalid", input: `5`, wantErr: true},
		{name: "malformed json", input: `["a",`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := countInputs(json.RawMessage(c.input), limit)
			if c.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, n)
		})
	}
}

// TestCountInputsOversizedBatchStopsEarly: a non-numeric batch past the limit is
// reported as over-limit without scanning further, and is clearly rejectable.
func TestCountInputsOversizedBatchStopsEarly(t *testing.T) {
	input := `["a","b","c","d","e"]`
	n, err := countInputs(json.RawMessage(input), 2)
	require.NoError(t, err)
	require.Greater(t, n, 2, "count must exceed the limit so AllowsBatch rejects it")
}

// TestCountInputsRejectsHugeArray: an all-number array beyond the scan cap is
// refused rather than scanned in full, bounding work on a hostile body.
func TestCountInputsRejectsHugeArray(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < maxInputArrayElements+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('0')
	}
	b.WriteByte(']')

	_, err := countInputs(json.RawMessage(b.String()), 25)
	require.ErrorIs(t, err, errInputTooLarge)
}

func TestParsePromptTokens(t *testing.T) {
	require.Equal(t, int64(42), parsePromptTokens([]byte(`{"usage":{"prompt_tokens":42}}`)))
	require.Equal(t, int64(0), parsePromptTokens([]byte(`{"data":[]}`)))     // missing usage
	require.Equal(t, int64(0), parsePromptTokens([]byte(`not json at all`))) // best-effort
}

// TestParsePromptTokensAcceptsBothAPIs: the OpenAI-compatible route reports
// usage.prompt_tokens and Ollama's native /api/embed reports prompt_eval_count.
// Both are the same bge-m3 input-token count, so both must bill identically —
// otherwise native-API traffic would be served for free.
func TestParsePromptTokensAcceptsBothAPIs(t *testing.T) {
	cases := map[string]struct {
		body string
		want int64
	}{
		"openai usage block": {`{"object":"list","data":[],"usage":{"prompt_tokens":7,"total_tokens":7}}`, 7},
		"native prompt_eval": {`{"model":"bge-m3","embeddings":[[0.1]],"prompt_eval_count":7}`, 7},
		"neither field":      {`{"model":"bge-m3","embeddings":[[0.1]]}`, 0},
		"not json":           {`<html>502 bad gateway</html>`, 0},
		"zero tokens":        {`{"usage":{"prompt_tokens":0}}`, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.want, parsePromptTokens([]byte(c.body)))
		})
	}
}
