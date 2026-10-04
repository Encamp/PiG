//go:build pig_bedrock

package ai

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

func bedrockMatrixEmitter(t *testing.T, w http.ResponseWriter) func(string, any) {
	t.Helper()
	encoder := eventstream.NewEncoder()
	return func(kind string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
			return
		}
		headers := eventstream.Headers{}
		headers.Set(":message-type", eventstream.StringValue("event"))
		headers.Set(":event-type", eventstream.StringValue(kind))
		headers.Set(":content-type", eventstream.StringValue("application/json"))
		if err := encoder.Encode(w, eventstream.Message{Headers: headers, Payload: raw}); err != nil {
			t.Error(err)
		}
	}
}

func bedrockPayloadHasZeroTemperature(payload any) bool {
	if input, ok := payload.(*bedrockruntime.ConverseStreamInput); ok {
		return input.InferenceConfig != nil && input.InferenceConfig.Temperature != nil && *input.InferenceConfig.Temperature == 0
	}
	return false
}

func bedrockStreamPayload(t *testing.T, payload any) (map[string]any, error) {
	t.Helper()
	input, ok := payload.(*bedrockruntime.ConverseStreamInput)
	if !ok {
		t.Fatalf("payload = %T", payload)
	}
	observed := map[string]any{}
	if input.AdditionalModelRequestFields != nil {
		data, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
		if err != nil {
			return nil, err
		}
		var fields any
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		observed["additionalModelRequestFields"] = fields
	}
	if input.RequestMetadata != nil {
		encoded, err := json.Marshal(input.RequestMetadata)
		if err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, err
		}
		observed["requestMetadata"] = value
	}
	return observed, nil
}
