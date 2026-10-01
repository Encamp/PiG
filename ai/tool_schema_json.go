package ai

import (
	"encoding/json"
	"maps"
)

// toolSchemaJSON writes a tool schema in the key order its declaration used, which a Go map cannot keep, so a provider sends the schema as pi does. rootKeys, when given, orders the top-level object in place of order. Nil means no order applies, so json.Marshal(schema) already writes the same bytes.
func toolSchemaJSON(schema map[string]any, order schemaObjectOrder, rootKeys ...string) (json.RawMessage, error) {
	if schema == nil || order == nil && rootKeys == nil {
		return nil, nil
	}
	if rootKeys != nil {
		rooted := make(schemaObjectOrder, len(order)+1)
		maps.Copy(rooted, order)
		rooted[""] = rootKeys
		order = rooted
	}
	if _, err := json.Marshal(schema); err != nil {
		return nil, err
	}
	return marshalSchemaWithOrder(schema, order, "")
}
