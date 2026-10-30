package ai

import "github.com/MichaelKinsy/PiG/internal/stackprime"

func (builder *assistantStreamBuilder) runResponse(run func() error) {
	defer recoverStream(builder.partial.Provider, builder.failPanic)
	stackprime.Prime()
	if builder.abort != nil {
		defer builder.abort()
	}
	if err := run(); err != nil {
		reason := StopReasonError
		if builder.ctx.Err() != nil {
			reason = StopReasonAborted
		}
		_ = builder.responseTurn(func() error {
			builder.failUnfinished(reason, err)
			return nil
		})
	}
}
