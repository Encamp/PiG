//go:build pig_bedrock

package ai

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// smithyResponseStatus returns the HTTP status an AWS SDK response error carries.
func smithyResponseStatus(err error) *int {
	if response, ok := errors.AsType[*smithyhttp.ResponseError](err); ok {
		return new(response.HTTPStatusCode())
	}
	return nil
}

type connectionError interface {
	ConnectionError() bool
}

// mapBedrockTransportError applies the same categories at the AWS SDK boundary. Modeled HTTP/service errors remain untouched so status and Bedrock exception classification continue to control overflow and retry behavior.
func mapBedrockTransportError(ctx context.Context, err error, message string) error {
	if err == nil {
		return nil
	}
	if contextErr := contextTransportError(ctx); contextErr != nil {
		return contextErr
	}
	if !isGoTransportError(err) {
		return err
	}
	return &nodeTransportError{message: message, cause: err}
}

func isGoTransportError(err error) bool {
	var connection connectionError
	if errors.As(err, &connection) && connection.ConnectionError() {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	// HTTP/2 errors in net/http are intentionally unexported and can reach an SDK stream without a net.Error in their chain. Keep this fallback limited to Go transport messages rather than broad provider text.
	text := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection reset by peer",
		"use of closed network connection",
		"http2: server sent goaway",
		"http2: client connection lost",
		"http2 stream closed",
		"tls handshake timeout",
	} {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}
