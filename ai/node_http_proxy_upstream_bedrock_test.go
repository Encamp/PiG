//go:build pig_bedrock

package ai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// .upstream/v0.87.1/packages/ai/test/node-http-proxy.test.ts:57 — prefers scoped proxy env aliases before process env aliases.
// Drive the production Bedrock request rather than only a resolver helper.
func TestBedrockScopedProxyRequest(t *testing.T) {
	clearNodeProxyEnv(t)
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Host != "bedrock.example.invalid" {
			t.Errorf("proxy request target = %q", r.URL.Host)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"fixture rejection"}`)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	provider := NewBedrockProvider("anthropic.claude-test", "http://bedrock.example.invalid")
	stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hello")}}}), StreamOptions{Env: ProviderEnv{
		"HTTP_PROXY": proxy.URL, "AWS_REGION": "us-east-1", "AWS_BEDROCK_SKIP_AUTH": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result := stream.Result(); result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "fixture rejection") {
		t.Fatalf("want fixture HTTP rejection event, got %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("scoped proxy received %d requests, want 1; error: %v", requests.Load(), err)
	}
}
