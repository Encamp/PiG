//go:build pig_bedrock

package ai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// Upstream: packages/ai/test/bedrock-cache-write-1h-cost.test.ts. Separate 1h details surround a 5m detail; neither last-value assignment nor summing all TTLs is correct.
func TestBedrockStreamCacheWrite1hCost(t *testing.T) {
	var response bytes.Buffer
	encoder := eventstream.NewEncoder()
	for _, event := range []struct{ kind, payload string }{
		{"messageStart", `{"role":"assistant"}`},
		{"metadata", `{"usage":{"inputTokens":100,"outputTokens":5,"totalTokens":1000105,"cacheWriteInputTokens":1000000,"cacheDetails":[{"ttl":"1h","inputTokens":150000},{"ttl":"5m","inputTokens":600000},{"ttl":"1h","inputTokens":250000}]}}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
	} {
		var headers eventstream.Headers
		headers.Set(":message-type", eventstream.StringValue("event"))
		headers.Set(":event-type", eventstream.StringValue(event.kind))
		headers.Set(":content-type", eventstream.StringValue("application/json"))
		if err := encoder.Encode(&response, eventstream.Message{Headers: headers, Payload: []byte(event.payload)}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(response.Bytes())
	}))
	defer server.Close()

	// The real SDK decodes the fixture, but neither ambient profiles nor credential discovery may reach an external service.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	provider := NewBedrockProvider("us.anthropic.claude-opus-4-8", server.URL)
	defer func() { _ = provider.Close() }()
	stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{
		CacheRetention: CacheRetentionNone,
		Env:            ProviderEnv{"AWS_BEDROCK_SKIP_AUTH": "1", "AWS_REGION": "us-east-1"},
		ModelCost:      ModelCost{Input: 5, CacheWrite: 6.25},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStreamCacheWriteCost(t, stream, 400_000, 7.75)
}
