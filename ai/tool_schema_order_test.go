package ai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// declaredOrderSchemas are tool parameter schemas declared out of Go's sorted key order, with the bytes pi 0.87.1 sends for each:
// tool.parameters as JSON.stringify writes it, and Anthropic's {type, properties, required} (anthropic-messages.ts convertTools).
var declaredOrderSchemas = []struct{ name, declared, parameters, anthropic string }{
	{
		name:       "path",
		declared:   `{"type":"object","properties":{"path":{"type":"string","description":"x"}},"required":["path"]}`,
		parameters: `{"type":"object","properties":{"path":{"type":"string","description":"x"}},"required":["path"]}`,
		anthropic:  `{"type":"object","properties":{"path":{"type":"string","description":"x"}},"required":["path"]}`,
	},
	{
		name:       "nested",
		declared:   `{"properties":{"b":{"type":"string","description":"x"},"a":{"anyOf":[{"type":"string","enum":["z","y"]},{"type":"null"}]},"10":{"type":"number"},"9":{"type":"number"}},"type":"object","required":["b","a"],"additionalProperties":false}`,
		parameters: `{"properties":{"9":{"type":"number"},"10":{"type":"number"},"b":{"type":"string","description":"x"},"a":{"anyOf":[{"type":"string","enum":["z","y"]},{"type":"null"}]}},"type":"object","required":["b","a"],"additionalProperties":false}`,
		anthropic:  `{"type":"object","properties":{"9":{"type":"number"},"10":{"type":"number"},"b":{"type":"string","description":"x"},"a":{"anyOf":[{"type":"string","enum":["z","y"]},{"type":"null"}]}},"required":["b","a"]}`,
	},
}

// sentToolSchema returns the schema the request body declares for the tool named probe, as the body carries it.
func sentToolSchema(t *testing.T, body []byte) []byte {
	t.Helper()
	var found []byte
	var walk func(json.RawMessage)
	walk = func(raw json.RawMessage) {
		raw = bytes.TrimSpace(raw)
		switch {
		case len(raw) > 0 && raw[0] == '{':
			var object map[string]json.RawMessage
			_ = json.Unmarshal(raw, &object)
			if string(object["name"]) == `"probe"` {
				for _, key := range []string{"input_schema", "parameters", "parametersJsonSchema"} {
					if value, ok := object[key]; ok && found == nil {
						found = value
					}
				}
			}
			for _, value := range object {
				walk(value)
			}
		case len(raw) > 0 && raw[0] == '[':
			var items []json.RawMessage
			_ = json.Unmarshal(raw, &items)
			for _, item := range items {
				walk(item)
			}
		}
	}
	walk(body)
	return found
}

// firstRequestBody streams one turn that declares tool and returns the request body the provider sent.
func firstRequestBody(t *testing.T, api API, tool ToolSchema) []byte {
	t.Helper()
	var mu sync.Mutex
	var body []byte
	client := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "zstd" {
			decoder, err := zstd.NewReader(r.Body)
			if err != nil {
				return nil, err
			}
			defer decoder.Close()
			reader = decoder
		}
		b, _ := io.ReadAll(reader)
		mu.Lock()
		body = b
		mu.Unlock()
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stop"}}`)), Request: r}, nil
	})}
	provider := fetchOptionProvider(api, "https://request.test/v1")
	opts := StreamOptions{Fetch: client, Transport: TransportSSE}
	if api == APIGoogleGenerativeAI {
		provider.(*googleProvider).client = client
		opts.Fetch = nil
	}
	messages := []Message{UserMessage{Content: UserText("go"), Timestamp: 1}}
	// The refusal ends the turn, at setup or in the stream depending on the provider; only the body matters.
	if stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: messages, Tools: []ToolSchema{tool}}), opts); err == nil {
		stream.Result()
	}
	mu.Lock()
	defer mu.Unlock()
	if body == nil {
		t.Fatal("no request was sent")
	}
	return body
}

func TestToolSchemaSentInDeclarationOrder(t *testing.T) {
	for _, api := range []API{APIAnthropicMessages, APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIMistralConversations, APIGoogleGenerativeAI, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			for _, schema := range declaredOrderSchemas {
				want := schema.parameters
				if api == APIAnthropicMessages {
					want = schema.anthropic
				}
				t.Run(schema.name, func(t *testing.T) {
					var tool ToolSchema
					if err := json.Unmarshal([]byte(`{"name":"probe","description":"p","parameters":`+schema.declared+`}`), &tool); err != nil {
						t.Fatal(err)
					}
					t.Run("decoded", func(t *testing.T) {
						if got := sentToolSchema(t, firstRequestBody(t, api, tool)); string(got) != want {
							t.Fatalf("sent schema = %s, want %s", got, want)
						}
					})
					// A session line carries the declaration through ToToolDeclaration, an encode and a decode.
					t.Run("session-line", func(t *testing.T) {
						line, err := json.Marshal(ToToolDeclaration(tool))
						if err != nil {
							t.Fatal(err)
						}
						var reread ToolSchema
						if err := json.Unmarshal(line, &reread); err != nil {
							t.Fatal(err)
						}
						if got := sentToolSchema(t, firstRequestBody(t, api, reread)); string(got) != want {
							t.Fatalf("sent schema = %s, want %s", got, want)
						}
					})
				})
			}
		})
	}
}

// pi declares the placeholder literally (anthropic-messages.ts DEFERRED_TOOL_PLACEHOLDER), so its schema keeps type, properties, required.
func TestAnthropicDeferredPlaceholderMatchesPi(t *testing.T) {
	const want = `{"name":"__pi_deferred_placeholder__","description":"Reserved placeholder. Never available. Never call this.","input_schema":{"type":"object","properties":{},"required":[]},"defer_loading":true}`
	got, err := json.Marshal(deferredToolPlaceholder())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("placeholder = %s, want %s", got, want)
	}
}
