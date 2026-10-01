package ai

import (
	"bytes"
	"encoding/json"

	"github.com/MichaelKinsy/PiG/internal/jsonstringify"
)

// rawToolArguments returns text as JSON.stringify(JSON.parse(text)) would write it, keeping the model's key order, when that differs from Go's encoding of arguments. Nil means json.Marshal(arguments) already writes the same bytes, or text is not a complete JSON object.
func rawToolArguments(text []byte, arguments JsonObject) json.RawMessage {
	text = bytes.TrimSpace(text)
	if len(text) == 0 || text[0] != '{' || !json.Valid(text) {
		return nil
	}
	canonical, err := jsonstringify.Canonicalize(text)
	if err != nil {
		return nil
	}
	if encoded, err := json.Marshal(arguments); err == nil && bytes.Equal(encoded, canonical) {
		return nil
	}
	return canonical
}
