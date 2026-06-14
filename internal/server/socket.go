package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gartnera/gofin/ent"
	"github.com/sj14/jellyfin-go/api"
)

const (
	// socketKeepAliveSeconds is the keepalive timeout advertised to the client in
	// the initial ForceKeepAlive: the client should send a KeepAlive within this
	// window. Matches Jellyfin's default.
	socketKeepAliveSeconds int32 = 60
	// socketReadTimeout closes a connection that has sent nothing (not even a
	// KeepAlive) for twice the advertised window — a dead client.
	socketReadTimeout = 2 * time.Duration(socketKeepAliveSeconds) * time.Second
	// socketSendBuffer bounds a single connection's outbound queue; a client that
	// can't keep up has messages dropped rather than blocking the broadcaster.
	socketSendBuffer = 16
	// socketCoalesceWindow collapses a burst of index mutations (e.g. a bulk file
	// copy producing many single-file Index calls) into a single LibraryChanged
	// broadcast.
	socketCoalesceWindow = 200 * time.Millisecond
)

// socketHub tracks live WebSocket connections and fans server-side events out to
// them. It is created standalone (NewSocketHub) so the serve command can wire it
// into the scanner's change hook before constructing the HTTP server.
type socketHub struct {
	mu          sync.Mutex
	conns       map[*socketConn]struct{}
	notifyTimer *time.Timer
}

// NewSocketHub returns an empty hub. Exported so cmd/gofin can share one hub
// instance between the scanner's change hook and the HTTP server.
func NewSocketHub() *socketHub {
	return &socketHub{conns: map[*socketConn]struct{}{}}
}

func (h *socketHub) register(c *socketConn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *socketHub) unregister(c *socketConn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// broadcast queues payload to every connection without blocking; a connection
// whose buffer is full (a slow client) drops the message.
func (h *socketHub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.send <- payload:
		default:
		}
	}
}

// NotifyLibraryChanged schedules a single LibraryChanged broadcast. Repeated
// calls within socketCoalesceWindow collapse into one, so a scan touching many
// files doesn't flood clients. It is the func wired into scanner.WithChangeHook;
// it only arms a timer, so it is safe to call while the scanner holds its lock.
func (h *socketHub) NotifyLibraryChanged() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifyTimer != nil {
		return
	}
	h.notifyTimer = time.AfterFunc(socketCoalesceWindow, func() {
		h.mu.Lock()
		h.notifyTimer = nil
		h.mu.Unlock()
		h.broadcast(libraryChangedPayload())
	})
}

// socketConn is one client's connection. All writes funnel through writePump
// (coder/websocket permits only one concurrent writer), so handlers enqueue onto
// send rather than writing directly.
type socketConn struct {
	conn      *websocket.Conn
	user      *ent.User
	sessionID string
	send      chan []byte
}

// enqueue queues payload for the write pump, dropping it if the buffer is full.
func (c *socketConn) enqueue(payload []byte) {
	select {
	case c.send <- payload:
	default:
	}
}

// handleSocket upgrades GET /socket to a WebSocket and runs Jellyfin's keepalive
// protocol. requireAuth has already validated the api_key query param and placed
// the user in the context.
func (s *Server) handleSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The HTTP API already serves any origin (permissive CORS) and the socket
		// is token-authenticated, so skip origin verification.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept has already written an error response.
		return
	}

	c := &socketConn{
		conn:      conn,
		user:      userFrom(r.Context()),
		sessionID: tokenFromRequest(r),
		send:      make(chan []byte, socketSendBuffer),
	}
	s.hub.register(c)
	defer s.hub.unregister(c)
	defer conn.CloseNow()

	// Lifecycle is detached from the (post-hijack) request context: the read loop
	// returning on client disconnect cancels ctx, which stops the write pump.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.writePump(ctx)

	// Prompt the client to begin its keepalive timer.
	c.enqueue(forceKeepAlivePayload(socketKeepAliveSeconds))

	c.readLoop(ctx)
}

// writePump serialises all writes to the connection.
func (c *socketConn) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(wctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// readLoop reads inbound messages until the client disconnects or stops sending
// keepalives within socketReadTimeout.
func (c *socketConn) readLoop(ctx context.Context) {
	for {
		rctx, cancel := context.WithTimeout(ctx, socketReadTimeout)
		_, data, err := c.conn.Read(rctx)
		cancel()
		if err != nil {
			return
		}
		c.handleInbound(data)
	}
}

// handleInbound replies to KeepAlive and ignores subscription messages
// (SessionsStart, ScheduledTasksInfoStart, etc.) and anything else — gofin
// pushes only LibraryChanged.
func (c *socketConn) handleInbound(data []byte) {
	var env struct {
		MessageType string `json:"MessageType"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	if api.SessionMessageType(env.MessageType) == api.SESSIONMESSAGETYPE_KEEP_ALIVE {
		c.enqueue(outboundKeepAlivePayload())
	}
}

func forceKeepAlivePayload(seconds int32) []byte {
	m := api.NewForceKeepAliveMessage()
	m.SetMessageType(api.SESSIONMESSAGETYPE_FORCE_KEEP_ALIVE)
	m.SetData(seconds)
	b, _ := json.Marshal(m)
	return b
}

func outboundKeepAlivePayload() []byte {
	m := api.NewOutboundKeepAliveMessage()
	m.SetMessageType(api.SESSIONMESSAGETYPE_KEEP_ALIVE)
	b, _ := json.Marshal(m)
	return b
}

func libraryChangedPayload() []byte {
	m := api.NewLibraryChangedMessage()
	m.SetMessageType(api.SESSIONMESSAGETYPE_LIBRARY_CHANGED)
	m.SetData(*api.NewLibraryUpdateInfo())
	b, _ := json.Marshal(m)
	return b
}
