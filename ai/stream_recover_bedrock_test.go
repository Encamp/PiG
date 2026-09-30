//go:build pig_bedrock

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// TestBedrockStreamDecoderPanicEndsStream panics inside Bedrock's stream goroutine, at its second event, and requires the stream to end with one error event that names the provider and the panic.
func TestBedrockStreamDecoderPanicEndsStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		encoder := eventstream.NewEncoder()
		for _, event := range []struct {
			name string
			body any
		}{
			{"messageStart", map[string]any{"role": "assistant"}},
			{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "hello"}}},
			{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
			{"messageStop", map[string]any{"stopReason": "end_turn"}},
			{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}, "metrics": map[string]any{"latencyMs": 1}}},
		} {
			raw, err := json.Marshal(event.body)
			if err != nil {
				t.Error(err)
				return
			}
			headers := eventstream.Headers{}
			headers.Set(":message-type", eventstream.StringValue("event"))
			headers.Set(":event-type", eventstream.StringValue(event.name))
			headers.Set(":content-type", eventstream.StringValue("application/json"))
			if err := encoder.Encode(w, eventstream.Message{Headers: headers, Payload: raw}); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	var events atomic.Int32
	hook := func(AssistantMessageEvent) {
		if events.Add(1) == 2 {
			panic("synthetic decoder fault")
		}
	}
	ctx := context.WithValue(t.Context(), streamDecoderHookKey{}, hook)
	provider := NewBedrockProvider("anthropic.claude-test", server.URL)
	stream, err := provider.Stream(ctx, streamPanicTranscript(), StreamOptions{Env: ProviderEnv{"AWS_BEDROCK_SKIP_AUTH": "1", "AWS_REGION": "us-east-1"}})
	if err != nil {
		t.Fatal(err)
	}
	failures, result := drainPanicStream(t, stream)
	const want = "provider amazon-bedrock goroutine panicked: synthetic decoder fault"
	if len(failures) != 1 || failures[0].Reason != StopReasonError || failures[0].Error.ErrorMessage != want {
		t.Fatalf("error events = %#v, want one with %q", failures, want)
	}
	if failures[0].Error.API != APIBedrockConverseStream || failures[0].Error.Provider != "amazon-bedrock" {
		t.Fatalf("error identity = %q/%q", failures[0].Error.API, failures[0].Error.Provider)
	}
	if result == nil || result.StopReason != StopReasonError || result.ErrorMessage != want {
		t.Fatalf("result = %#v, want the panic error", result)
	}
}
