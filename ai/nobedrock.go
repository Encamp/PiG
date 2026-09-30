//go:build !pig_bedrock

package ai

import (
	"context"
	"fmt"
)

// BedrockProvider is the Amazon Bedrock provider of a binary built without the pig_bedrock build tag. It keeps Bedrock's identity so catalog, auth and model selection behave as in a tagged build, and every Stream call returns ErrBedrockNotBuilt.
type BedrockProvider struct {
	model         string
	modelName     string
	baseURL       string
	selectedModel *Model
}

// NewBedrockProvider constructs a Bedrock provider for the given model id.
func NewBedrockProvider(model, baseURL string) *BedrockProvider {
	return &BedrockProvider{model: model, baseURL: baseURL}
}

// NewBedrockProviderWithName constructs a Bedrock provider with optional display/model name metadata.
func NewBedrockProviderWithName(model, modelName, baseURL string) *BedrockProvider {
	return &BedrockProvider{model: model, modelName: modelName, baseURL: baseURL}
}

// NewBedrockProviderWithModel retains the selected model's identity.
func NewBedrockProviderWithModel(model Model) *BedrockProvider {
	return &BedrockProvider{model: model.ID, modelName: model.DisplayName, baseURL: model.ProviderMeta.BaseURL, selectedModel: &model}
}

// ID returns the selected model's provider identity, or the canonical Bedrock identity for constructors without model metadata.
func (p *BedrockProvider) ID() string {
	if p.selectedModel != nil {
		return modelProviderID(p.selectedModel)
	}
	return "amazon-bedrock"
}

// Close is a no-op; the provider holds no resources.
func (p *BedrockProvider) Close() error { return nil }

// Stream returns ErrBedrockNotBuilt: this binary does not link the AWS SDK.
func (p *BedrockProvider) Stream(context.Context, TranscriptContext, StreamOptions) (*AssistantMessageEventStream, error) {
	return nil, fmt.Errorf("%s model %q: %w", p.ID(), p.model, ErrBedrockNotBuilt)
}

func init() {
	builtInProviders[APIBedrockConverseStream] = func(_, model, baseURL string) Provider {
		return NewBedrockProvider(model, baseURL)
	}
}

// smithyResponseStatus reports no status: without the AWS SDK no error carries a smithy response.
func smithyResponseStatus(error) *int { return nil }
