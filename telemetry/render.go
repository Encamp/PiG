package telemetry

// Ports packages/telemetry/src/render.ts.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/MichaelKinsy/PiG/internal/jsnumber"
	"github.com/MichaelKinsy/PiG/internal/jsonstringify"
)

// formatAttributes is upstream's formatAttributes: Object.entries in insertion order.
func formatAttributes(attributes SpanAttributes) string {
	var text strings.Builder
	for _, attribute := range attributes {
		text.WriteString(" " + attribute.Name + "=" + jsonStringify(attribute.Value))
	}
	return text.String()
}

// jsonStringify is JSON.stringify of one attribute value.
func jsonStringify(value AttributeValue) string {
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprint(value)
	}
	canonical, err := jsonstringify.Canonicalize(raw.Bytes())
	if err != nil {
		return strings.TrimSpace(raw.String())
	}
	return string(canonical)
}

// RenderTrace renders stored spans as a chronological forest, treating absent parents as roots. Spans sharing an ID are
// one span, the last value in the first position, as upstream's Map keeps it; the input is
// not modified.
func RenderTrace(spans []StoredSpan) string {
	byID := map[string]StoredSpan{}
	var order []string
	for _, span := range spans {
		if _, ok := byID[span.ID]; !ok {
			order = append(order, span.ID)
		}
		byID[span.ID] = span
	}
	sorted := make([]StoredSpan, 0, len(order))
	for _, id := range order {
		sorted = append(sorted, byID[id])
	}
	sort.SliceStable(sorted, func(left, right int) bool { return sorted[left].StartedMs < sorted[right].StartedMs })

	var roots []StoredSpan
	children := map[string][]StoredSpan{}
	for _, span := range sorted {
		if span.ParentID == nil {
			roots = append(roots, span)
		} else if _, ok := byID[*span.ParentID]; !ok {
			roots = append(roots, span)
		} else {
			children[*span.ParentID] = append(children[*span.ParentID], span)
		}
	}

	type pendingSpan struct {
		span  StoredSpan
		depth int
	}
	var lines []string
	slices.Reverse(roots)
	pending := make([]pendingSpan, 0, len(roots))
	for _, root := range roots {
		pending = append(pending, pendingSpan{root, 0})
	}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		span, indent := current.span, strings.Repeat("  ", current.depth)
		failure := ""
		if span.Status.Status == SpanStatusCodeError {
			failure = " error"
			if span.Status.Error != nil {
				failure += " " + span.Status.Error.Name + ": " + span.Status.Error.Message
			}
		}
		lines = append(lines, indent+span.Name+formatAttributes(span.Attributes)+" ("+jsnumber.String(span.EndedMs-span.StartedMs)+"ms)"+failure)
		for _, event := range span.Events {
			lines = append(lines, indent+"  · "+event.Name+formatAttributes(event.Attributes)+" (+"+jsnumber.String(event.TimeMs)+"ms)")
		}
		for _, link := range span.Links {
			lines = append(lines, indent+"  → link trace "+link.TraceID+" span "+link.SpanID+formatAttributes(link.Attributes))
		}
		descendants := children[span.ID]
		for index := len(descendants) - 1; index >= 0; index-- {
			pending = append(pending, pendingSpan{descendants[index], current.depth + 1})
		}
	}
	return strings.Join(lines, "\n")
}
