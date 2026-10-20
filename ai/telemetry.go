package ai

// Ports packages/ai/src/telemetry.ts.
//
// Pi's typed-schema helpers are type-level: SchemaTelemetrySpan and the span-name literal types have no Go form, so the
// request span is the plain telemetry.TelemetrySpan and the typed starter and typed metrics are the runtime helpers of
// package telemetry.
//
// Async contract of observeModelRequest (telemetry.ts:84-86, `void start(...)`): the span's callback is a Promise that
// settles when the observer's result is observed, and nobody awaits it. Go starts the callback on a launched goroutine
// that blocks until onResult runs (or the adapter's StartSpan returns), and the observer waits only until the callback
// has run so that its span is set before the first event, as Pi's synchronous adapters guarantee. Ordering: events and
// the result are observed on the forwarding turn, after the outer stream's push/end; the span ends after the result
// is observed, on the launched goroutine. Errors: a failed StartSpan leaves the no-op span in place and records
// nothing. Cancellation: the observer has no cancellation of its own; it ends with the stream's terminal result, which
// the forwarding always reaches.

import (
	"sync"

	"github.com/MichaelKinsy/PiG/internal/jsclock"
	"github.com/MichaelKinsy/PiG/internal/jsstring"
	"github.com/MichaelKinsy/PiG/telemetry"
)

var (
	aiProvider = telemetry.TelemetryStartAttributeDefinition{
		TelemetryAttributeDefinition: telemetry.TelemetryAttributeDefinition{
			TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: "Provider identifier."},
			Type:                       telemetry.TelemetryAttributeTypeString,
		},
		Required: true,
	}
	aiModel = telemetry.TelemetryStartAttributeDefinition{
		TelemetryAttributeDefinition: telemetry.TelemetryAttributeDefinition{
			TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: "Requested model identifier."},
			Type:                       telemetry.TelemetryAttributeTypeString,
		},
		Required: true,
	}
	aiStop = telemetry.TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: "Normalized stop reason."},
		Type:                       telemetry.TelemetryAttributeTypeString,
		Values:                     []telemetry.AttributeValue{"pending", "stop", "length", "toolUse", "error", "aborted", "deferred"},
	}
)

func aiAttribute(valueType telemetry.TelemetryAttributeType, description string, required bool) telemetry.TelemetryStartAttributeDefinition {
	return telemetry.TelemetryStartAttributeDefinition{
		TelemetryAttributeDefinition: telemetry.TelemetryAttributeDefinition{
			TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: description},
			Type:                       valueType,
		},
		Required: required,
	}
}

func aiEndAttribute(description string) telemetry.TelemetryAttributeDefinition {
	return telemetry.TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: description},
		Type:                       telemetry.TelemetryAttributeTypeNumber,
	}
}

func aiRequestSpan(description string) telemetry.TelemetrySpanDefinition {
	blockType := aiAttribute(telemetry.TelemetryAttributeTypeString, "Block kind.", true)
	blockType.Values = []telemetry.AttributeValue{"text", "thinking", "tool_call"}
	responseID := telemetry.TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: telemetry.TelemetryAttributeMetadata{Description: "Provider response identifier.", Cardinality: telemetry.TelemetryAttributeCardinalityHigh},
		Type:                       telemetry.TelemetryAttributeTypeString,
	}
	return telemetry.TelemetrySpanDefinition{
		Description: description,
		Parents:     telemetry.TelemetryParentDefinition{Kind: telemetry.TelemetryParentKindAny},
		StartAttributes: map[string]telemetry.TelemetryStartAttributeDefinition{
			"provider":  aiProvider,
			"api":       aiAttribute(telemetry.TelemetryAttributeTypeString, "API identifier.", true),
			"model":     aiModel,
			"reasoning": aiAttribute(telemetry.TelemetryAttributeTypeString, "Scalar requested reasoning level.", false),
		},
		EndAttributes: map[string]telemetry.TelemetryAttributeDefinition{
			"stop":              aiStop,
			"usage.input":       aiEndAttribute("Uncached input tokens."),
			"usage.output":      aiEndAttribute("Output tokens."),
			"usage.cache_read":  aiEndAttribute("Cache-read tokens."),
			"usage.cache_write": aiEndAttribute("Cache-write tokens."),
			"cost.total":        aiEndAttribute("Total request cost in USD."),
			"response.id":       responseID,
		},
		Events: map[string]telemetry.TelemetryEventDefinition{
			"pi.ai.first_chunk": {Description: "First normalized stream event delivered.", Attributes: map[string]telemetry.TelemetryEventAttributeDefinition{}},
			"pi.ai.block": {
				Description: "A completed content block; content is never recorded.",
				Attributes: map[string]telemetry.TelemetryEventAttributeDefinition{
					"type":  blockType,
					"index": aiAttribute(telemetry.TelemetryAttributeTypeNumber, "Content block index.", true),
					"chars": aiAttribute(telemetry.TelemetryAttributeTypeNumber, "UTF-16 character count.", true),
					"tool":  aiAttribute(telemetry.TelemetryAttributeTypeString, "Tool name, for tool-call blocks only.", false),
				},
			},
		},
		Status: telemetry.TelemetrySpanStatusDefinition{
			Default:   telemetry.SpanStatusCodeOK,
			ErrorWhen: "Stop reason is error: provider_error with at most 500 error-message characters. Aborted stays ok.",
		},
	}
}

func aiMetricStop() telemetry.TelemetryMetricAttributeDefinition {
	stop := aiAttribute(telemetry.TelemetryAttributeTypeString, aiStop.Description, true)
	stop.Values = aiStop.Values
	return stop
}

// AITelemetrySchema is Pi's AI_TELEMETRY_SCHEMA: Pi-owned request metadata, bounded error status, events, and
// low-cardinality metrics.
var AITelemetrySchema = telemetry.DefineTelemetrySchema(&telemetry.TelemetrySchemaDefinition{
	Version: 1,
	Spans: map[string]telemetry.TelemetrySpanDefinition{
		"pi.ai.request": aiRequestSpan("One logical model request, including authentication and provider setup."),
		"pi.ai.poll":    aiRequestSpan("One deferred-response poll, not the deferred request's lifetime."),
	},
	Metrics: map[string]telemetry.TelemetryMetricDefinition{
		"pi.ai.request.duration": {
			Kind:        telemetry.TelemetryMetricKindHistogram,
			Description: "Logical request or poll duration.",
			Unit:        new("ms"),
			Attributes:  map[string]telemetry.TelemetryMetricAttributeDefinition{"provider": aiProvider, "model": aiModel, "stop": aiMetricStop()},
		},
		"pi.ai.first_chunk.duration": {
			Kind:        telemetry.TelemetryMetricKindHistogram,
			Description: "Time to the first normalized stream event.",
			Unit:        new("ms"),
			Attributes:  map[string]telemetry.TelemetryMetricAttributeDefinition{"provider": aiProvider, "model": aiModel},
		},
		"pi.ai.tokens": {
			Kind:        telemetry.TelemetryMetricKindCounter,
			Description: "Reported model token usage; pending and deferred polls are excluded.",
			Unit:        new("token"),
			Attributes: map[string]telemetry.TelemetryMetricAttributeDefinition{
				"provider": aiProvider,
				"model":    aiModel,
				"kind": func() telemetry.TelemetryMetricAttributeDefinition {
					kind := aiAttribute(telemetry.TelemetryAttributeTypeString, "Token usage category.", true)
					kind.Values = []telemetry.AttributeValue{"input", "output", "cache_read", "cache_write"}
					return kind
				}(),
			},
		},
		"pi.ai.cost": {
			Kind:        telemetry.TelemetryMetricKindCounter,
			Description: "Reported model cost; pending and deferred polls are excluded.",
			Unit:        new("USD"),
			Attributes:  map[string]telemetry.TelemetryMetricAttributeDefinition{"provider": aiProvider, "model": aiModel},
		},
	},
})

type aiInstruments struct {
	requestDuration, firstChunkDuration telemetry.TelemetryHistogram
	tokens, cost                        telemetry.TelemetryCounter
}

func createAIInstruments(adapter telemetry.TelemetryMetrics) *aiInstruments {
	metrics := telemetry.CreateTypedMetrics(adapter, AITelemetrySchema)
	return &aiInstruments{
		requestDuration:    metrics.Histogram("pi.ai.request.duration"),
		firstChunkDuration: metrics.Histogram("pi.ai.first_chunk.duration"),
		tokens:             metrics.Counter("pi.ai.tokens"),
		cost:               metrics.Counter("pi.ai.cost"),
	}
}

// aiInstrumentCache is Pi's instrumentCache WeakMap<TelemetryMetrics, instruments>, keyed by the adapter's identity.
// Go has no weak map over interface keys, so an adapter stays referenced for the process lifetime; an adapter whose
// dynamic type is not comparable cannot key a map and creates its instruments per request.
var aiInstrumentCache sync.Map

func aiInstrumentsFor(adapter telemetry.TelemetryMetrics) *aiInstruments {
	defer func() { _ = recover() }() // an uncomparable dynamic type panics in Load and Store
	if cached, ok := aiInstrumentCache.Load(adapter); ok {
		return cached.(*aiInstruments)
	}
	instruments, _ := aiInstrumentCache.LoadOrStore(adapter, createAIInstruments(adapter))
	return instruments.(*aiInstruments)
}

// modelRequestObservation is the state observeModelRequest closes over.
type modelRequestObservation struct {
	started    float64
	attributes telemetry.SpanAttributes
	poll       bool
	instr      *aiInstruments
	span       telemetry.TelemetrySpan
	finished   chan struct{}
	first      bool
	toolChars  map[int]int
}

// observeModelRequest is Pi's observeModelRequest. launch runs the background span callback, as Pi's unawaited start(...) call; a nil observer is returned
// for the no-op span, which has nothing to record (Pi's observer does the same work against no-op capabilities).
// options nil is Pi's omitted options.
func observeModelRequest(requestedModel *Model, options *StreamOptions, poll bool, launch func(func())) *LazyStreamObserver {
	var parent telemetry.TelemetrySpan
	if options != nil {
		parent = options.Telemetry
	}
	if parent == nil || parent == telemetry.NoopTelemetryContext {
		return nil
	}
	o := &modelRequestObservation{
		started:    jsclock.PerformanceNow(),
		attributes: telemetry.SpanAttributes{"provider": modelProviderID(requestedModel), "model": requestedModel.ID},
		poll:       poll,
		span:       telemetry.NoopTelemetryContext,
		finished:   make(chan struct{}),
		first:      true,
	}
	adapter := parent.Metrics()
	if adapter == nil {
		adapter = telemetry.NoopTelemetryMetrics
	}
	o.instr = aiInstrumentsFor(adapter)
	if o.instr == nil {
		o.instr = createAIInstruments(adapter)
	}
	startAttributes := telemetry.SpanAttributes{"provider": o.attributes["provider"], "model": o.attributes["model"], "api": string(requestedModel.ProviderMeta.API)}
	if options.Thinking != "" {
		startAttributes["reasoning"] = string(options.Thinking)
	}
	name := "pi.ai.request"
	if poll {
		name = "pi.ai.poll"
	}
	start := telemetry.CreateTypedSpanStarter(parent, AITelemetrySchema)
	ready := make(chan struct{})
	var readyOnce sync.Once
	markReady := func() { readyOnce.Do(func() { close(ready) }) }
	launch(func() {
		defer markReady()
		_ = start(name, startAttributes, func(span telemetry.TelemetrySpan, _ telemetry.TypedSpanStarter) error {
			o.span = span
			markReady()
			<-o.finished
			return nil
		})
	})
	<-ready
	return &LazyStreamObserver{OnEvent: o.onEvent, OnResult: o.onResult}
}

func (o *modelRequestObservation) onEvent(event AssistantMessageEvent) {
	if o.first {
		o.first = false
		o.span.AddEvent("pi.ai.first_chunk", nil)
		o.instr.firstChunkDuration.Record(jsclock.PerformanceNow()-o.started, o.attributes, o.span)
	}
	switch event := event.(type) {
	case ToolCallDeltaEvent:
		if o.toolChars == nil {
			o.toolChars = map[int]int{}
		}
		o.toolChars[event.ContentIndex] += jsstring.Length(event.Delta)
	case TextEndEvent:
		o.span.AddEvent("pi.ai.block", telemetry.SpanAttributes{"type": "text", "index": event.ContentIndex, "chars": jsstring.Length(event.Content)})
	case ThinkingEndEvent:
		o.span.AddEvent("pi.ai.block", telemetry.SpanAttributes{"type": "thinking", "index": event.ContentIndex, "chars": jsstring.Length(event.Content)})
	case ToolCallEndEvent:
		chars := o.toolChars[event.ContentIndex]
		delete(o.toolChars, event.ContentIndex)
		o.span.AddEvent("pi.ai.block", telemetry.SpanAttributes{"type": "tool_call", "index": event.ContentIndex, "chars": chars, "tool": event.ToolCall.Name})
	}
}

func (o *modelRequestObservation) metricAttributes(extra string, value string) telemetry.SpanAttributes {
	attributes := telemetry.SpanAttributes{"provider": o.attributes["provider"], "model": o.attributes["model"]}
	if extra != "" {
		attributes[extra] = value
	}
	return attributes
}

func (o *modelRequestObservation) onResult(message *AssistantMessage) {
	if message != nil {
		attributes := telemetry.SpanAttributes{
			"stop":              string(message.StopReason),
			"usage.input":       message.Usage.Input,
			"usage.output":      message.Usage.Output,
			"usage.cache_read":  message.Usage.CacheRead,
			"usage.cache_write": message.Usage.CacheWrite,
			"cost.total":        message.Usage.Cost.Total,
		}
		if message.ResponseID != "" {
			attributes["response.id"] = message.ResponseID
		}
		o.span.SetAttributes(attributes)
		if message.StopReason == StopReasonError {
			o.span.SetStatus(telemetry.SpanStatus{Status: telemetry.SpanStatusCodeError, Error: &telemetry.SpanStatusError{Name: "provider_error", Message: jsstring.Slice(message.ErrorMessage, 0, 500)}})
		}
		o.instr.requestDuration.Record(jsclock.PerformanceNow()-o.started, o.metricAttributes("stop", string(message.StopReason)), o.span)
		if !o.poll || (message.StopReason != StopReasonPending && message.StopReason != StopReasonDeferred) {
			o.instr.tokens.Add(float64(message.Usage.Input), o.metricAttributes("kind", "input"))
			o.instr.tokens.Add(float64(message.Usage.Output), o.metricAttributes("kind", "output"))
			o.instr.tokens.Add(float64(message.Usage.CacheRead), o.metricAttributes("kind", "cache_read"))
			o.instr.tokens.Add(float64(message.Usage.CacheWrite), o.metricAttributes("kind", "cache_write"))
			o.instr.cost.Add(message.Usage.Cost.Total, o.attributes)
		}
	}
	close(o.finished)
}
