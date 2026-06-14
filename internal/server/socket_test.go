package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsURL rewrites an http(s) test-server URL to its ws(s) equivalent.
func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// readMessageType reads one text frame and returns its MessageType field.
func readMessageType(ctx context.Context, t *testing.T, c *websocket.Conn) string {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct{ MessageType string }
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	return env.MessageType
}

func TestSocketHandshake(t *testing.T) {
	env := setupEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL(env.srv.URL)+"/socket?api_key="+env.token, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// The server greets with ForceKeepAlive.
	if mt := readMessageType(ctx, t, c); mt != "ForceKeepAlive" {
		t.Fatalf("first message = %q, want ForceKeepAlive", mt)
	}

	// A client KeepAlive is answered with a KeepAlive.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"MessageType":"KeepAlive"}`)); err != nil {
		t.Fatalf("write keepalive: %v", err)
	}
	if mt := readMessageType(ctx, t, c); mt != "KeepAlive" {
		t.Fatalf("keepalive reply = %q, want KeepAlive", mt)
	}
}

func TestSocketRequiresAuth(t *testing.T) {
	env := setupEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL(env.srv.URL)+"/socket", nil)
	if err == nil {
		t.Fatal("dial without token succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestSocketLibraryChanged(t *testing.T) {
	env := setupEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := authedClient(env.srv.URL, env.token)
	id := firstMovie(t, client)

	c, _, err := websocket.Dial(ctx, wsURL(env.srv.URL)+"/socket?api_key="+env.token, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// Consume the initial ForceKeepAlive.
	if mt := readMessageType(ctx, t, c); mt != "ForceKeepAlive" {
		t.Fatalf("first message = %q, want ForceKeepAlive", mt)
	}

	// Refreshing an item mutates the index through the scanner, whose change hook
	// drives a LibraryChanged broadcast.
	resp := postAuthed(t, env, "/Items/"+id+"/Refresh")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("refresh status = %d, want 204", resp.StatusCode)
	}

	// Expect a LibraryChanged (ignoring any interleaved keepalive traffic).
	for {
		if mt := readMessageType(ctx, t, c); mt == "LibraryChanged" {
			break
		}
	}
}
