package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/auth"
	"github.com/sj14/jellyfin-go/api"
)

const (
	// quickConnectTTL is how long a pending request stays valid before it is pruned.
	quickConnectTTL = 10 * time.Minute
	// quickConnectMaxPending caps the number of in-flight handshakes. Initiate is
	// unauthenticated, so without a ceiling it would be a trivial way to exhaust
	// the server's memory. It also keeps the 6-digit code space sparse, so the
	// collision-retry loop stays cheap and can never spin.
	quickConnectMaxPending = 100
)

// errQuickConnectFull is returned by initiate when the pending-request cap is hit.
var errQuickConnectFull = errors.New("too many pending quick connect requests")

// qcRequest is one in-flight Quick Connect handshake. The originating device
// holds the opaque secret and polls; an already-authenticated user approves the
// short human-facing code, which binds the request to that user.
type qcRequest struct {
	secret     string
	code       string
	deviceID   string
	deviceName string
	appName    string
	appVersion string
	added      time.Time
	authorized bool
	user       *ent.User
}

// quickConnectStore holds pending Quick Connect requests in memory, mirroring
// upstream Jellyfin — these are short-lived and need not survive a restart.
type quickConnectStore struct {
	mu   sync.Mutex
	reqs map[string]*qcRequest // keyed by secret
}

func newQuickConnectStore() *quickConnectStore {
	return &quickConnectStore{reqs: map[string]*qcRequest{}}
}

// generateCode returns a random 6-digit numeric code, matching the short code
// Jellyfin shows the user.
func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// pruneLocked drops expired requests. Callers must hold q.mu.
func (q *quickConnectStore) pruneLocked() {
	cutoff := time.Now().Add(-quickConnectTTL)
	for secret, req := range q.reqs {
		if req.added.Before(cutoff) {
			delete(q.reqs, secret)
		}
	}
}

// codeInUseLocked reports whether a pending request already uses code. Callers
// must hold q.mu.
func (q *quickConnectStore) codeInUseLocked(code string) bool {
	for _, req := range q.reqs {
		if req.code == code {
			return true
		}
	}
	return false
}

// initiate creates a new pending request for the originating device and returns
// a snapshot of it.
func (q *quickConnectStore) initiate(deviceID, deviceName, appName, appVersion string) (qcRequest, error) {
	secret, err := auth.GenerateToken()
	if err != nil {
		return qcRequest{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked()
	if len(q.reqs) >= quickConnectMaxPending {
		return qcRequest{}, errQuickConnectFull
	}
	var code string
	for attempt := 0; ; attempt++ {
		code, err = generateCode()
		if err != nil {
			return qcRequest{}, err
		}
		if !q.codeInUseLocked(code) {
			break
		}
		// Unreachable in practice (cap << code space), but never spin.
		if attempt >= quickConnectMaxPending {
			return qcRequest{}, errQuickConnectFull
		}
	}
	req := &qcRequest{
		secret:     secret,
		code:       code,
		deviceID:   deviceID,
		deviceName: deviceName,
		appName:    appName,
		appVersion: appVersion,
		added:      time.Now(),
	}
	q.reqs[secret] = req
	return *req, nil
}

// state returns a snapshot of the request identified by secret.
func (q *quickConnectStore) state(secret string) (qcRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked()
	req, ok := q.reqs[secret]
	if !ok {
		return qcRequest{}, false
	}
	return *req, true
}

// authorize binds the request identified by code to u, marking it authorized.
// It reports whether a matching pending request was found.
func (q *quickConnectStore) authorize(code string, u *ent.User) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked()
	for _, req := range q.reqs {
		if req.code == code {
			req.authorized = true
			req.user = u
			return true
		}
	}
	return false
}

// consume returns the bound user for an authorized request and removes it, so a
// secret can be exchanged for an access token exactly once.
func (q *quickConnectStore) consume(secret string) (*ent.User, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked()
	req, ok := q.reqs[secret]
	if !ok || !req.authorized || req.user == nil {
		return nil, false
	}
	delete(q.reqs, secret)
	return req.user, true
}

// qcResult maps a request snapshot to the wire model.
func qcResult(req qcRequest) *api.QuickConnectResult {
	res := api.NewQuickConnectResult()
	res.SetAuthenticated(req.authorized)
	res.SetSecret(req.secret)
	res.SetCode(req.code)
	res.SetDeviceId(req.deviceID)
	res.SetDeviceName(req.deviceName)
	res.SetAppName(req.appName)
	res.SetAppVersion(req.appVersion)
	res.SetDateAdded(req.added)
	return res
}

// handleQuickConnectEnabled reports whether Quick Connect is available.
func (s *Server) handleQuickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.quickConnectEnabled)
}

// handleQuickConnectInitiate starts a new handshake for the requesting device.
// Device identity is taken from the MediaBrowser authorization header so the
// approving user can tell which device is asking.
func (s *Server) handleQuickConnectInitiate(w http.ResponseWriter, r *http.Request) {
	if !s.quickConnectEnabled {
		http.Error(w, "Quick Connect is disabled", http.StatusUnauthorized)
		return
	}
	pairs := ParseAuthorization(r.Header.Get("Authorization"))
	if len(pairs) == 0 {
		pairs = ParseAuthorization(r.Header.Get("X-Emby-Authorization"))
	}
	req, err := s.quickConnect.initiate(pairs["DeviceId"], pairs["Device"], pairs["Client"], pairs["Version"])
	if errors.Is(err, errQuickConnectFull) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, qcResult(req))
}

// handleQuickConnectState lets the originating device poll for authorization.
func (s *Server) handleQuickConnectState(w http.ResponseWriter, r *http.Request) {
	if !s.quickConnectEnabled {
		http.Error(w, "Quick Connect is disabled", http.StatusUnauthorized)
		return
	}
	secret := r.URL.Query().Get("secret")
	req, ok := s.quickConnect.state(secret)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, qcResult(req))
}

// handleQuickConnectAuthorize approves a pending code on behalf of the
// authenticated user, binding the request to their account.
func (s *Server) handleQuickConnectAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.quickConnectEnabled {
		http.Error(w, "Quick Connect is disabled", http.StatusUnauthorized)
		return
	}
	u := userFrom(r.Context())
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ok := s.quickConnect.authorize(code, u)
	writeJSON(w, http.StatusOK, ok)
}

// handleAuthenticateWithQuickConnect exchanges an authorized secret for an
// access token, completing the handshake the same way a password login would.
func (s *Server) handleAuthenticateWithQuickConnect(w http.ResponseWriter, r *http.Request) {
	if !s.quickConnectEnabled {
		http.Error(w, "Quick Connect is disabled", http.StatusUnauthorized)
		return
	}
	var body api.QuickConnectDto
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, ok := s.quickConnect.consume(body.GetSecret())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	result, err := s.issueAccessToken(r, u)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
