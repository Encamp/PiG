package ai

import (
	"fmt"
	"time"
)

// recoverStream turns a panic on a goroutine that serves a stream into that stream's terminal error, so one provider's defect ends one call rather than the process. Every goroutine the package starts for a stream defers it as its first statement; fail must end the stream without waiting on the goroutine that panicked.
func recoverStream(provider string, fail func(error)) {
	value := recover()
	if value == nil {
		return
	}
	fail(fmt.Errorf("provider %s goroutine panicked: %v", provider, value))
}

// runRecovered is run for a turn started on its own goroutine: a panic in body ends the stream through fail.
func (turn *continuationTurn) runRecovered(provider string, fail func(error), body func(*continuationTurn)) {
	defer recoverStream(provider, fail)
	turn.run(body)
}

// failStreamPanic ends a stream that has no builder with err after a recovered panic, carrying the stream's identity and no content.
func failStreamPanic(stream *AssistantMessageEventStream, api API, provider, model string, err error) {
	_ = stream.Push(ErrorEvent{Reason: StopReasonError, Error: &AssistantMessage{
		Content: []AssistantContentBlock{}, API: api, Provider: provider, Model: model,
		StopReason: StopReasonError, ErrorMessage: err.Error(), Timestamp: time.Now().UnixMilli(),
	}})
}

// failPanic ends the stream with err after a recovered panic. The panic already released any executor turn the goroutine held, so it pushes straight to the stream rather than waiting for a turn. A stream that already ended keeps its terminal, because Push ignores later events.
func (builder *assistantStreamBuilder) failPanic(err error) {
	message, err := builder.panicMessage(err)
	message.StopReason = StopReasonError
	message.ErrorMessage = err.Error()
	_ = builder.stream.Push(ErrorEvent{Reason: StopReasonError, Error: message})
}

// panicMessage copies the partial the panicked goroutine left behind, without its streaming scratch. A partial that cannot be copied is replaced by an empty message with the stream's identity, and the second panic joins the error.
func (builder *assistantStreamBuilder) panicMessage(err error) (message *AssistantMessage, result error) {
	result = err
	fresh := func() *AssistantMessage {
		empty := &AssistantMessage{Content: []AssistantContentBlock{}}
		if partial := builder.partial; partial != nil {
			empty.API, empty.Provider, empty.Model, empty.Timestamp = partial.API, partial.Provider, partial.Model, partial.Timestamp
		}
		return empty
	}
	defer func() {
		if value := recover(); value != nil {
			message = fresh()
			result = fmt.Errorf("%w; copying its partial message panicked: %v", err, value)
		}
	}()
	if builder.partial == nil {
		return fresh(), err
	}
	copied := cloneAssistantMessage(*builder.partial)
	for i, content := range copied.Content {
		switch block := content.(type) {
		case ToolCall:
			block.scratch = toolCallScratch{}
			copied.Content[i] = block
		case TextContent:
			block.scratch = ""
			copied.Content[i] = block
		case ThinkingContent:
			block.scratch = ""
			copied.Content[i] = block
		}
	}
	return &copied, err
}
