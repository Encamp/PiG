//go:build !pig_bedrock

package ai

import (
	"net/http"
	"testing"
)

func bedrockMatrixEmitter(t *testing.T, _ http.ResponseWriter) func(string, any) {
	t.Helper()
	t.Fatal("Bedrock matrix requires -tags pig_bedrock")
	return nil
}

func bedrockPayloadHasZeroTemperature(any) bool { return false }

func bedrockStreamPayload(t *testing.T, _ any) (map[string]any, error) {
	t.Helper()
	t.Fatal("Bedrock payload requires -tags pig_bedrock")
	return nil, nil
}
