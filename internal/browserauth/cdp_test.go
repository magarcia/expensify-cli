package browserauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

type fakeSocket struct {
	writes [][]byte
	reads  [][]byte
}

func (s *fakeSocket) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}
func (s *fakeSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	if len(s.reads) == 0 {
		return 0, nil, io.EOF
	}
	data := s.reads[0]
	s.reads = s.reads[1:]
	return websocket.MessageText, data, nil
}
func (*fakeSocket) CloseNow() error { return nil }

func TestCDPMatchesResponsesAndScopesCommandsToSession(t *testing.T) {
	socket := &fakeSocket{reads: [][]byte{
		[]byte(`{"method":"Target.targetInfoChanged","params":{"targetInfo":{}}}`),
		[]byte(`{"id":1,"result":{"value":"result"}}`),
	}}
	connection := &cdp{socket: socket}
	var result struct{ Value string }
	if err := connection.Call(context.Background(), "Runtime.evaluate", map[string]string{"expression": "test"}, "owned-session", &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "result" {
		t.Fatalf("got result %q", result.Value)
	}
	var sent map[string]any
	if err := json.Unmarshal(socket.writes[0], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["sessionId"] != "owned-session" || sent["id"] != float64(1) {
		t.Fatalf("incorrectly scoped command: %v", sent)
	}
}

func TestCDPDoesNotExposeRemoteErrorContent(t *testing.T) {
	socket := &fakeSocket{reads: [][]byte{[]byte(`{"id":1,"error":{"code":-32000,"message":"credential-secret"}}`)}}
	connection := &cdp{socket: socket}
	err := connection.Call(context.Background(), "Runtime.evaluate", nil, "", nil)
	var failure *protocolError
	if !errors.As(err, &failure) || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("unsafe browser error: %v", err)
	}
}

func TestDialRejectsNonLoopbackAndRedirectStyleEndpoints(t *testing.T) {
	for _, endpoint := range []string{"wss://127.0.0.1:1234/devtools/browser/id", "ws://localhost:1234/devtools/browser/id", "ws://evil.example:1234/devtools/browser/id", "ws://127.0.0.1:1234/devtools/page/id", "ws://127.0.0.1:1234/devtools/browser/id?secret=value", "ws://user:secret@127.0.0.1:1234/devtools/browser/id"} {
		if connection, err := dialCDP(context.Background(), endpoint); err == nil || connection != nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
}
