package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexWebSocketObserverPanicEndsStreamAndClosesConnection(t *testing.T) {
	closed := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = connection.Close(); close(closed) }()
		if _, _, err := connection.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		for _, frame := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg-1","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`,
		} {
			if err := connection.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				t.Error(err)
				return
			}
		}
		// A failed observer must release the session connection, including its reader.
		_, _, _ = connection.ReadMessage()
	}))
	t.Cleanup(server.Close)
	sessionID := t.Name()
	t.Cleanup(func() { CloseOpenAICodexWebSocketSessions(sessionID) })
	provider := NewOpenAICodexResponsesProvider(OpenAICodexResponsesConfig{APIKey: codexTestToken(t, "panic-account"), Model: "gpt-5.2", ProviderID: "openai-codex", BaseURL: server.URL})
	observed := 0
	stream, err := provider.Stream(t.Context(), streamPanicTranscript(), StreamOptions{Transport: TransportWebSocket, SessionID: sessionID, OnProviderStreamEvent: func(context.Context, any, *Model) error {
		observed++
		if observed == 2 {
			panic("websocket observer fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	failures, result := drainPanicStream(t, stream)
	if len(failures) != 1 || result.StopReason != StopReasonError || result.ErrorMessage != "provider openai-codex goroutine panicked: websocket observer fault" {
		t.Fatalf("terminal = %#v, errors = %#v", result, failures)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("panicked websocket stream retained its connection")
	}
}
