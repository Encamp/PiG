package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Each text is a tool list exactly as pi 0.87.1's JSON.stringify writes it, with
// the length, system-message tokens and max-tokens clamp pi's own estimate.js
// and simple-options.js computed for it. encoding/json escapes <, > and & as
// six-character sequences unless asked not to; JSON.stringify leaves them.
func TestToolSchemaJSONStringifyMatchesPi(t *testing.T) {
	for _, tc := range []struct {
		name          string
		text          string
		length        int
		tokens        int
		clampedTokens int
	}{
		// Keys out of alphabetical order, so the declared order is kept and written key by key.
		{"declared-order", `[{"name":"edit","description":"Edit <path> & keep rev_<n> current.","parameters":{"type":"object","properties":{"rev":{"type":"string","description":"rev_<n> from read"},"a<b>&c":{"type":"string","enum":["<",">","&"]}},"required":["rev","a<b>&c"]}}]`, 249, 63, 937},
		// Every object already alphabetical, so no declared order is recorded.
		{"alphabetical", `[{"name":"note","description":"a & b","parameters":{"properties":{"tag":{"description":"<b>","type":"string"}},"type":"object"}}]`, 129, 33, 967},
		{"grammar", `[{"name":"emit","description":"x","parameters":{"type":"object"},"constrainedSampling":{"type":"grammar","variants":{"openai_lark":"start: a -> b & <c>"}}}]`, 156, 39, 961},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tools []ToolSchema
			if err := json.Unmarshal([]byte(tc.text), &tools); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(tools); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSuffix(buf.String(), "\n"); got != tc.text {
				t.Errorf("encoded = %s\nwant      %s", got, tc.text)
			}
			if got := JSONStringifyLength(tools); got != tc.length {
				t.Errorf("JSONStringifyLength = %d, want %d", got, tc.length)
			}
			context := NormalizeContext(Context{Tools: tools})
			if got := EstimateMessageTokens(context.Messages()[0]); got != tc.tokens {
				t.Errorf("system message tokens = %d, want %d", got, tc.tokens)
			}
			model := &Model{Capabilities: ModelCapabilities{ContextWindow: 5096}}
			if got := ClampMaxTokensToContext(model, context, 32_000); got != tc.clampedTokens {
				t.Errorf("max tokens = %d, want %d", got, tc.clampedTokens)
			}
		})
	}
}
