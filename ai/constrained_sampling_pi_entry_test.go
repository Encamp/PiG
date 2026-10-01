package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// A pi 0.87.1 session writes each built-in tool's declaration with `constrainedSampling: false` in the system entry's toolsAdded.
func TestToolSchemaDecodesPiSystemEntryConstrainedSamplingFalse(t *testing.T) {
	tool := func(name string) string {
		return `{"name":"` + name + `","description":"` + name + ` a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"constrainedSampling":false}`
	}
	entry := `{"type":"message","id":"5f0c2a1e","parentId":"9b3d7c40","timestamp":"2026-09-30T12:00:00.000Z","message":{"role":"system","content":"You are a test agent.","toolsAdded":[` +
		tool("read") + `,` + tool("bash") + `,` + tool("edit") + `,` + tool("write") + `],"timestamp":1759233600000}}`
	var decoded struct {
		Message struct {
			Role       string       `json:"role"`
			ToolsAdded []ToolSchema `json:"toolsAdded"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(entry), &decoded); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, schema := range decoded.Message.ToolsAdded {
		names = append(names, schema.Name)
		if schema.ConstrainedSampling != nil || !schema.ConstrainedSamplingDisabled {
			t.Errorf("%s: config=%v disabled=%v", schema.Name, schema.ConstrainedSampling, schema.ConstrainedSamplingDisabled)
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != tool(schema.Name) {
			t.Errorf("re-encoded %s = %s", schema.Name, encoded)
		}
	}
	if strings.Join(names, ",") != "read,bash,edit,write" {
		t.Fatalf("toolsAdded = %v", names)
	}
}
