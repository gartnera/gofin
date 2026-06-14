// Package discovery implements the Jellyfin/Emby UDP client auto-discovery
// protocol. Clients broadcast a small UTF-8 datagram ("Who is JellyfinServer?")
// to UDP port 7359 on the local network; a server listening on that port
// replies, unicast, with a JSON blob describing how to reach it. This is what
// lets the "Connect" screen in stock clients find a server without the user
// typing its address.
//
// The wire format is a port of Jellyfin's AutoDiscoveryHost: the JSON fields are
// PascalCase (Address, Id, Name, EndpointAddress) to match .NET's default
// System.Text.Json serialization, and EndpointAddress is always null — emitted,
// not omitted — exactly as upstream does.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
)

// Port is the UDP port Jellyfin/Emby clients broadcast discovery requests to.
const Port = 7359

// Trigger substrings. Jellyfin matches "who is JellyfinServer?" case-insensitively
// as a substring of the datagram; we also answer the historical Emby variant so
// Emby-derived clients discover gofin too.
const (
	jellyfinQuery = "who is jellyfinserver?"
	embyQuery     = "who is embyserver?"
)

// info is the discovery response payload. Field names and casing must match
// Jellyfin's ServerDiscoveryInfo so stock clients parse it. EndpointAddress is a
// pointer without omitempty so it serializes as `"EndpointAddress":null`, as
// upstream emits.
type info struct {
	Address         string  `json:"Address"`
	Id              string  `json:"Id"`
	Name            string  `json:"Name"`
	EndpointAddress *string `json:"EndpointAddress"`
}

// Server answers UDP auto-discovery broadcasts on Port. It advertises the HTTP
// base URL reachable from each requester, plus the server's id and name.
type Server struct {
	id       string
	name     string
	httpPort int
}

// New constructs a discovery responder. id and name should match what the HTTP
// server reports in /System/Info (use server.DeriveServerID for the id);
// httpPort is the TCP port the HTTP API listens on, advertised in the response
// URL.
func New(id, name string, httpPort int) *Server {
	return &Server{id: id, name: name, httpPort: httpPort}
}

// ListenAndServe binds UDP Port on all interfaces and serves until ctx is
// cancelled. A bind failure (e.g. the port is taken) is returned to the caller.
func (s *Server) ListenAndServe(ctx context.Context) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: Port})
	if err != nil {
		return fmt.Errorf("bind udp/%d: %w", Port, err)
	}
	return s.Serve(ctx, conn)
}

// Serve reads discovery requests from conn and replies to each. It takes
// ownership of conn (closing it on return) so tests can supply an
// ephemeral-port socket. It returns nil on ctx cancellation.
func (s *Server) Serve(ctx context.Context, conn *net.UDPConn) error {
	// Unblock the read loop on cancellation by closing the socket.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 1024)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		msg := strings.ToLower(string(buf[:n]))
		if !strings.Contains(msg, jellyfinQuery) && !strings.Contains(msg, embyQuery) {
			continue
		}
		resp, err := s.response(remote.IP)
		if err != nil {
			log.Printf("discovery: build response for %s: %v", remote, err)
			continue
		}
		if _, err := conn.WriteToUDP(resp, remote); err != nil && ctx.Err() == nil {
			log.Printf("discovery: reply to %s: %v", remote, err)
		}
	}
}

// response builds the JSON reply for a request from remote, advertising the
// local address best reachable from that requester.
func (s *Server) response(remote net.IP) ([]byte, error) {
	local, err := localAddrFor(remote)
	if err != nil {
		return nil, err
	}
	addr := "http://" + net.JoinHostPort(local, strconv.Itoa(s.httpPort))
	return json.Marshal(info{Address: addr, Id: s.id, Name: s.name})
}

// localAddrFor returns the local IP the kernel would use to reach remote, so the
// advertised URL is reachable from the requester. This mirrors Jellyfin's
// GetBindAddress(remoteAddr): no packet is actually sent — connecting a UDP
// socket only resolves the route and picks the source address.
func localAddrFor(remote net.IP) (string, error) {
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: remote, Port: Port})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
