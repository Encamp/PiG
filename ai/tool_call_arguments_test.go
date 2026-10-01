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

const modelOrderArguments = `{"z":1,"a":{"y":2,"b":3}}`

func modelOrderSSE(api API) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	first, second := q(`{"z":1,"a":`), q(`{"y":2,"b":3}}`)
	switch api {
	case APIAnthropicMessages:
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"probe\",\"input\":{}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":" + first + "}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":" + second + "}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	case APIOpenAICompletions, APIMistralConversations:
		return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call1abcd\",\"type\":\"function\",\"function\":{\"name\":\"probe\",\"arguments\":" + first + "}}]},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":" + second + "}}]},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"
	case APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses:
		return "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"model\":\"test-model\",\"output\":[]}}\n\n" +
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"probe\",\"arguments\":\"\"}}\n\n" +
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":" + first + "}\n\n" +
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":" + second + "}\n\n" +
			"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"output_index\":0,\"arguments\":" + q(modelOrderArguments) + "}\n\n" +
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"probe\",\"arguments\":" + q(modelOrderArguments) + ",\"status\":\"completed\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"test-model\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
	case APIGoogleGenerativeAI:
		return "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"probe\",\"args\":" + modelOrderArguments + "}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n"
	case APIPiMessages:
		return "data: {\"type\":\"start\"}\n\n" +
			"data: {\"type\":\"toolcall_start\",\"contentIndex\":0,\"id\":\"call_1\",\"toolName\":\"probe\"}\n\n" +
			"data: {\"type\":\"toolcall_delta\",\"contentIndex\":0,\"delta\":" + first + "}\n\n" +
			"data: {\"type\":\"toolcall_delta\",\"contentIndex\":0,\"delta\":" + second + "}\n\n" +
			"data: {\"type\":\"toolcall_end\",\"contentIndex\":0}\n\n" +
			"data: {\"type\":\"done\",\"reason\":\"toolUse\",\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":2,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}}}\n\n"
	}
	panic(api)
}

// sentArguments returns the second request's tool-call arguments as the body carries them.
func sentArguments(t *testing.T, api API, body []byte) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	var found []byte
	var walk func(json.RawMessage)
	walk = func(raw json.RawMessage) {
		raw = bytes.TrimSpace(raw)
		switch {
		case len(raw) > 0 && raw[0] == '{':
			var object map[string]json.RawMessage
			_ = json.Unmarshal(raw, &object)
			for key, value := range object {
				switch key {
				case "input", "args":
					if bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
						found = value
					}
				case "arguments":
					var text string
					if json.Unmarshal(value, &text) == nil {
						found = []byte(text)
					} else if bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
						found = value
					}
				}
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

func TestToolCallRawArgumentsResentByteEqual(t *testing.T) {
	for _, api := range []API{APIAnthropicMessages, APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIMistralConversations, APIGoogleGenerativeAI, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			var mu sync.Mutex
			var bodies [][]byte
			client := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
				var body io.Reader = r.Body
				if r.Header.Get("Content-Encoding") == "zstd" {
					decoder, err := zstd.NewReader(r.Body)
					if err != nil {
						return nil, err
					}
					defer decoder.Close()
					body = decoder
				}
				b, _ := io.ReadAll(body)
				mu.Lock()
				bodies = append(bodies, b)
				n := len(bodies)
				mu.Unlock()
				if n == 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(modelOrderSSE(api))), Request: r}, nil
				}
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stop"}}`)), Request: r}, nil
			})}
			provider := fetchOptionProvider(api, "https://request.test/v1")
			opts := StreamOptions{Fetch: client, Transport: TransportSSE}
			if api == APIGoogleGenerativeAI {
				provider.(*googleProvider).client = client
				opts.Fetch = nil
			}
			tool := ToolSchema{Name: "probe", Description: "p", Parameters: map[string]any{"type": "object"}}
			messages := []Message{UserMessage{Content: UserText("go"), Timestamp: 1}}
			stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: messages, Tools: []ToolSchema{tool}}), opts)
			if err != nil {
				t.Fatal(err)
			}
			result := stream.Result()
			var call ToolCall
			for _, block := range result.Content {
				if value, ok := block.(ToolCall); ok {
					call = value
				}
			}
			messages = append(messages, *result, ToolResultMessage{ToolCallID: call.ID, ToolName: "probe", Content: []ToolResultMessageContent{TextContent{Text: "ok"}}, Timestamp: 3})
			if stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: messages, Tools: []ToolSchema{tool}}), opts); err == nil {
				stream.Result()
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("requests = %d", len(bodies))
			}
			if got := sentArguments(t, api, bodies[1]); string(got) != modelOrderArguments {
				t.Fatalf("re-sent arguments = %s, want %s", got, modelOrderArguments)
			}
		})
	}
}

// Google's functionCall omitted args for nil and empty argument maps before args could carry ordered text, and still does.
func TestGoogleToolCallOmitsEmptyArgs(t *testing.T) {
	for name, arguments := range map[string]JsonObject{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var body []byte
			client := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				body = b
				mu.Unlock()
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stop"}}`)), Request: r}, nil
			})}
			provider := fetchOptionProvider(APIGoogleGenerativeAI, "https://request.test/v1")
			provider.(*googleProvider).client = client
			tool := ToolSchema{Name: "probe", Description: "p", Parameters: map[string]any{"type": "object"}}
			messages := []Message{
				UserMessage{Content: UserText("go"), Timestamp: 1},
				AssistantMessage{Content: []AssistantContentBlock{ToolCall{ID: "call_1", Name: "probe", Arguments: arguments}}, API: APIGoogleGenerativeAI, Provider: "google", Model: "test-model", StopReason: StopReasonToolUse, Timestamp: 2},
				ToolResultMessage{ToolCallID: "call_1", ToolName: "probe", Content: []ToolResultMessageContent{TextContent{Text: "ok"}}, Timestamp: 3},
			}
			if stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: messages, Tools: []ToolSchema{tool}}), StreamOptions{Transport: TransportSSE}); err == nil {
				stream.Result()
			}
			mu.Lock()
			defer mu.Unlock()
			if !bytes.Contains(body, []byte(`"functionCall":{"name":"probe"`)) {
				t.Fatalf("request carries no functionCall: %s", body)
			}
			if bytes.Contains(body, []byte(`"args"`)) {
				t.Fatalf("request sends args for %s arguments: %s", name, body)
			}
		})
	}
}

func TestToolCallRawArgumentsLineRoundTrip(t *testing.T) {
	line := func(arguments string) string {
		return `{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"toolCall","id":"call_1","name":"probe","arguments":` + arguments + `}],"api":"anthropic-messages","provider":"anthropic","model":"claude-x","usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":3,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":1759300000000}`
	}
	for name, arguments := range map[string]string{
		"model-order":  modelOrderArguments,
		"sorted":       `{"a":1,"z":2}`,
		"html-escaped": `{"z":"a\u003cb","a":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			var message AssistantMessage
			if err := json.Unmarshal([]byte(line(arguments)), &message); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != line(arguments) {
				t.Fatalf("marshal = %s\nwant      %s", encoded, line(arguments))
			}
		})
	}
}

func TestRawToolArgumentsMatchesJSONStringify(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"whitespace", `{"z": 1, "a": {"y": 2, "b": 3}}`, modelOrderArguments},
		{"integer-keys", `{"b":1,"2":0,"a":{"10":1,"9":2}}`, `{"2":0,"b":1,"a":{"9":2,"10":1}}`},
		{"duplicate-key", `{"z":1,"a":2,"z":3}`, `{"z":3,"a":2}`},
		{"escapes", `{"z":"\u00e9\/","a":1}`, `{"z":"é/","a":1}`},
		{"numbers", `{"z":1.0,"a":1E2}`, `{"z":1,"a":100}`},
		{"truncated", `{"z":1,"a":{"y":2`, ""},
		{"not-object", `[1,2]`, ""},
		{"sorted-is-nil", `{"a":1,"z":2}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments := parseStreamingJsonObject(tc.text)
			if got := rawToolArguments([]byte(tc.text), arguments); string(got) != tc.want {
				t.Fatalf("rawToolArguments(%s) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
