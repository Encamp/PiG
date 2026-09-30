package ai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// retryPerCallFamily is one provider family's minimal successful stream.
type retryPerCallFamily struct {
	api  API
	body string
	make func(baseURL string) Provider
}

func retryPerCallFamilies() []retryPerCallFamily {
	return []retryPerCallFamily{
		{
			api: APIAnthropicMessages,
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			make: func(baseURL string) Provider {
				return NewAnthropicProvider(AnthropicConfig{APIKey: "test", Model: "claude-test", BaseURL: baseURL, ProviderID: "anthropic"})
			},
		},
		{
			api: APIOpenAICompletions,
			body: "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			make: func(baseURL string) Provider {
				return NewOpenAIProvider(OpenAIConfig{APIKey: "test", Model: "gpt-test", BaseURL: baseURL, ProviderID: "openai"})
			},
		},
		{
			api: APIOpenAIResponses,
			body: "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n",
			make: func(baseURL string) Provider {
				return NewOpenAIResponsesProvider(OpenAIResponsesConfig{APIKey: "test", Model: "gpt-test", BaseURL: baseURL, ProviderID: "openai"})
			},
		},
		{
			api:  APIGoogleGenerativeAI,
			body: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n",
			make: func(baseURL string) Provider {
				return NewGoogleProvider(GoogleConfig{APIKey: "test", Model: "gemini-test", BaseURL: baseURL, ProviderID: "google"})
			},
		},
	}
}

// TestStreamRetryPolicyIsPerCall runs three concurrent streams against one server that answers each stream's first request 529 with retry-after-ms 5 and its next request 200. Each stream's own maxRetries and maxRetryDelayMs decide its outcome: pi passes both per request (utils/provider-retry.ts), including through Google's retryGoogleRequest.
func TestStreamRetryPolicyIsPerCall(t *testing.T) {
	const streamHeader = "X-Retry-Probe"
	transcript := NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi"), Timestamp: 1}}})
	for _, family := range retryPerCallFamilies() {
		t.Run(string(family.api), func(t *testing.T) {
			policies := []struct {
				name                        string
				maxRetries, maxRetryDelayMs *int
				attempts                    int32
				reason                      StopReason
				errorContains               string
			}{
				{name: "A", maxRetries: new(1), attempts: 2, reason: StopReasonStop},
				{name: "B", attempts: 1, reason: StopReasonError},
				{name: "C", maxRetries: new(1), maxRetryDelayMs: new(1), attempts: 1, reason: StopReasonError, errorContains: "retry delay"},
			}
			attempts := map[string]*atomic.Int32{}
			for _, policy := range policies {
				attempts[policy.name] = new(atomic.Int32)
			}
			// The first requests wait until all three streams have one in flight, so no stream finishes before the others start.
			var arrived sync.WaitGroup
			arrived.Add(len(policies))
			allArrived := make(chan struct{})
			go func() { arrived.Wait(); close(allArrived) }()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				counter := attempts[r.Header.Get(streamHeader)]
				if counter == nil {
					t.Errorf("request without a known %s header: %q", streamHeader, r.Header.Get(streamHeader))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if counter.Add(1) == 1 {
					arrived.Done()
					select {
					case <-allArrived:
					case <-time.After(10 * time.Second):
						t.Error("the three first requests were not in flight together")
					}
					w.Header().Set("retry-after-ms", "5")
					w.WriteHeader(529)
					_, _ = io.WriteString(w, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, family.body)
			}))
			t.Cleanup(server.Close)

			results := make([]*AssistantMessage, len(policies))
			var streams sync.WaitGroup
			for index, policy := range policies {
				streams.Go(func() {
					options := StreamOptions{Headers: ProviderHeaders{streamHeader: new(policy.name)}, MaxRetries: policy.maxRetries, MaxRetryDelayMs: policy.maxRetryDelayMs}
					stream, err := family.make(server.URL).Stream(t.Context(), transcript, options)
					if err != nil {
						results[index] = &AssistantMessage{StopReason: StopReasonError, ErrorMessage: err.Error()}
						return
					}
					results[index] = stream.Result()
				})
			}
			streams.Wait()

			for index, policy := range policies {
				result := results[index]
				if got := attempts[policy.name].Load(); got != policy.attempts {
					t.Errorf("stream %s: %d attempts, want %d", policy.name, got, policy.attempts)
				}
				if result.StopReason != policy.reason {
					t.Errorf("stream %s: stop reason %q (%s), want %q", policy.name, result.StopReason, result.ErrorMessage, policy.reason)
				}
				if policy.errorContains != "" && !strings.Contains(result.ErrorMessage, policy.errorContains) {
					t.Errorf("stream %s: error %q, want it to contain %q", policy.name, result.ErrorMessage, policy.errorContains)
				}
			}
			if t.Failed() {
				t.Logf("configured default: %d retries", ProviderMaxRetries(t.Context()))
			}
		})
	}
}
