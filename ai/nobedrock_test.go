//go:build !pig_bedrock

package ai

import (
	"errors"
	"testing"
)

// BedrockBuilt reports whether this test binary links Amazon Bedrock. Untagged, the provider matrices leave out their Bedrock rows; the pig_bedrock build runs them.
const BedrockBuilt = false

func TestBedrockNotBuilt(t *testing.T) {
	models := ListModels("amazon-bedrock")
	if len(models) == 0 {
		t.Fatal("catalog lists no amazon-bedrock models")
	}
	model := models[0].ToModel()
	if model.ProviderMeta.API != APIBedrockConverseStream {
		t.Fatalf("catalog model %s has API %q", model.ID, model.ProviderMeta.API)
	}
	transcript := NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}})

	provider := NewBedrockProviderWithModel(*model)
	if provider.ID() != "amazon-bedrock" {
		t.Fatalf("ID() = %q, want amazon-bedrock", provider.ID())
	}
	stream, err := provider.Stream(t.Context(), transcript, StreamOptions{})
	if !errors.Is(err, ErrBedrockNotBuilt) || stream != nil {
		t.Fatalf("Stream = %v, %v; want nil, ErrBedrockNotBuilt", stream, err)
	}

	stream, err = StreamSimple(t.Context(), model, transcript, StreamOptions{})
	if !errors.Is(err, ErrBedrockNotBuilt) || stream != nil {
		t.Fatalf("StreamSimple = %v, %v; want nil, ErrBedrockNotBuilt", stream, err)
	}

	factory, ok := LookupBuiltInProvider(APIBedrockConverseStream)
	if !ok {
		t.Fatal("Bedrock provider not registered")
	}
	if _, err := factory("", model.ID, "").Stream(t.Context(), transcript, StreamOptions{}); !errors.Is(err, ErrBedrockNotBuilt) {
		t.Fatalf("registered provider Stream error = %v, want ErrBedrockNotBuilt", err)
	}
}
