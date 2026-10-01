package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

// Pi prints a failed call's arguments as JSON.stringify(toolCall.arguments, null, 2):
// the model's key order, two-space indent, and <, > and & left as they are.
// The expected text is pi 0.87.1's validateToolArguments output for pi's edit
// schema with one more required property that the call lacks.
func TestValidationErrorPrintsReceivedArgumentsInModelOrder(t *testing.T) {
	const want = "Validation failed for tool \"edit\":\n  - expectedRevision: must have required properties expectedRevision\n\nReceived arguments:\n{\n  \"path\": \"a<b\",\n  \"edits\": [\n    {\n      \"oldText\": \"x\",\n      \"newText\": \"y\"\n    }\n  ]\n}"
	var call ai.ToolCall
	if err := json.Unmarshal([]byte(`{"type":"toolCall","id":"call-1","name":"edit","arguments":{"path": "a<b", "edits": [{"oldText": "x", "newText": "y"}]}}`), &call); err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","required":["path","edits","expectedRevision"],"properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","required":["oldText","newText"],"properties":{"oldText":{"type":"string"},"newText":{"type":"string"}}}},"expectedRevision":{"type":"string"}}}`), &params); err != nil {
		t.Fatal(err)
	}
	executed := false
	tool := &scriptTool{name: "edit", params: params, execute: func(context.Context, string, json.RawMessage, ToolUpdateCallback) (AgentToolResult, error) {
		executed = true
		return AgentToolResult{}, nil
	}}
	a := NewAgent(AgentOptions{
		Model: scriptedModel(&scriptedProvider{respond: toolCallsThenText(call)}),
		Tools: []AgentTool{tool},
	})
	var got []string
	for _, message := range mustSend(t, a, "edit") {
		if message.ToolResult != nil {
			for _, block := range message.ToolResult.Content {
				if text, ok := block.(ai.TextContent); ok {
					got = append(got, text.Text)
				}
			}
		}
	}
	if executed {
		t.Fatal("tool executed with invalid arguments")
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("tool result = %q\nwant          %q", got, want)
	}
}
