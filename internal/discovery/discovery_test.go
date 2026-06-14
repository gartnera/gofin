package discovery

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// serveEphemeral starts a discovery responder on an ephemeral UDP port (so tests
// don't fight over the fixed 7359) and returns the bound address.
func serveEphemeral(t *testing.T, s *Server) *net.UDPAddr {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Serve(ctx, conn) }()
	return conn.LocalAddr().(*net.UDPAddr)
}

// query sends msg to addr and returns the reply, failing if none arrives.
func query(t *testing.T, addr *net.UDPAddr, msg string) []byte {
	t.Helper()
	client, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return buf[:n]
}

func TestRespondsToJellyfinQuery(t *testing.T) {
	addr := serveEphemeral(t, New("abc123", "myserver", 8096))
	reply := query(t, addr, "Who is JellyfinServer?")

	var got info
	if err := json.Unmarshal(reply, &got); err != nil {
		t.Fatalf("unmarshal reply %q: %v", reply, err)
	}
	if got.Id != "abc123" {
		t.Errorf("Id = %q, want abc123", got.Id)
	}
	if got.Name != "myserver" {
		t.Errorf("Name = %q, want myserver", got.Name)
	}
	// Loopback requester -> loopback advertised address with the HTTP port.
	if got.Address != "http://127.0.0.1:8096" {
		t.Errorf("Address = %q, want http://127.0.0.1:8096", got.Address)
	}
	if got.EndpointAddress != nil {
		t.Errorf("EndpointAddress = %v, want nil", got.EndpointAddress)
	}
}

func TestRespondsToEmbyQuery(t *testing.T) {
	addr := serveEphemeral(t, New("id", "srv", 8096))
	// Case-insensitive substring match, Emby variant.
	reply := query(t, addr, "hello, who is EMBYSERVER? please")
	if !strings.Contains(string(reply), `"Id":"id"`) {
		t.Errorf("expected discovery reply, got %q", reply)
	}
}

// PascalCase field names and an explicit null EndpointAddress are required for
// wire compatibility with Jellyfin's ServerDiscoveryInfo.
func TestReplyWireFormat(t *testing.T) {
	addr := serveEphemeral(t, New("id", "srv", 8096))
	reply := string(query(t, addr, "Who is JellyfinServer?"))
	for _, want := range []string{`"Address":`, `"Id":`, `"Name":`, `"EndpointAddress":null`} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply %q missing %q", reply, want)
		}
	}
}

func TestIgnoresUnknownDatagram(t *testing.T) {
	addr := serveEphemeral(t, New("id", "srv", 8096))
	client, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1024)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("expected no reply to an unrelated datagram")
	}
}
