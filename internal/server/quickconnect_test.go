package server_test

import (
	"context"
	"net/http"
	"testing"

	jfapi "github.com/sj14/jellyfin-go/api"
)

// TestQuickConnectFlow drives the full handshake with the real Jellyfin client:
// an anonymous device initiates, an authenticated session authorizes the code,
// and the device exchanges the secret for an access token.
func TestQuickConnectFlow(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()

	device := anonClient(env.srv.URL)

	enabled, _, err := device.QuickConnectAPI.GetQuickConnectEnabled(ctx).Execute()
	if err != nil {
		t.Fatalf("GetQuickConnectEnabled: %v", err)
	}
	if !enabled {
		t.Fatal("Quick Connect should be enabled by default")
	}

	// The device initiates and receives a secret + user-facing code.
	initiated, _, err := device.QuickConnectAPI.InitiateQuickConnect(ctx).Execute()
	if err != nil {
		t.Fatalf("InitiateQuickConnect: %v", err)
	}
	secret := initiated.GetSecret()
	code := initiated.GetCode()
	if secret == "" || code == "" {
		t.Fatalf("expected secret and code, got %+v", initiated)
	}
	if initiated.GetAuthenticated() {
		t.Fatal("freshly initiated request must not be authenticated")
	}
	if initiated.GetDeviceId() != "dev-1" {
		t.Errorf("DeviceId = %q, want dev-1", initiated.GetDeviceId())
	}

	// Before approval the device polls and sees it is still unauthorized.
	state, _, err := device.QuickConnectAPI.GetQuickConnectState(ctx).Secret(secret).Execute()
	if err != nil {
		t.Fatalf("GetQuickConnectState: %v", err)
	}
	if state.GetAuthenticated() {
		t.Fatal("request authorized before the user approved it")
	}

	// An already-authenticated session approves the code.
	ok, _, err := authedClient(env.srv.URL, env.token).QuickConnectAPI.
		AuthorizeQuickConnect(ctx).Code(code).Execute()
	if err != nil {
		t.Fatalf("AuthorizeQuickConnect: %v", err)
	}
	if !ok {
		t.Fatal("AuthorizeQuickConnect returned false for a valid code")
	}

	// The device now sees the request authorized.
	state, _, err = device.QuickConnectAPI.GetQuickConnectState(ctx).Secret(secret).Execute()
	if err != nil {
		t.Fatalf("GetQuickConnectState after authorize: %v", err)
	}
	if !state.GetAuthenticated() {
		t.Fatal("request not authorized after approval")
	}

	// And exchanges the secret for an access token.
	body := jfapi.NewQuickConnectDto(secret)
	res, _, err := device.UserAPI.AuthenticateWithQuickConnect(ctx).QuickConnectDto(*body).Execute()
	if err != nil {
		t.Fatalf("AuthenticateWithQuickConnect: %v", err)
	}
	if res.GetAccessToken() == "" {
		t.Fatal("empty access token from Quick Connect")
	}
	if u := res.GetUser(); u.GetName() != testUser {
		t.Errorf("authenticated as %q, want %q", u.GetName(), testUser)
	}

	// The token works against an authenticated endpoint.
	if _, _, err := authedClient(env.srv.URL, res.GetAccessToken()).
		UserViewsAPI.GetUserViews(ctx).Execute(); err != nil {
		t.Fatalf("token issued by Quick Connect rejected: %v", err)
	}

	// The secret is single-use: a second exchange fails.
	if _, resp, err := device.UserAPI.AuthenticateWithQuickConnect(ctx).
		QuickConnectDto(*body).Execute(); err == nil {
		t.Fatal("expected error reusing a consumed secret")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on reuse, got %v", resp)
	}
}

// TestQuickConnectDisabled verifies the endpoints reject requests when the
// feature is turned off.
func TestQuickConnectDisabled(t *testing.T) {
	env := setupEnvWithQuickConnect(t, false)
	ctx := context.Background()
	device := anonClient(env.srv.URL)

	enabled, _, err := device.QuickConnectAPI.GetQuickConnectEnabled(ctx).Execute()
	if err != nil {
		t.Fatalf("GetQuickConnectEnabled: %v", err)
	}
	if enabled {
		t.Fatal("Quick Connect should report disabled")
	}

	if _, resp, err := device.QuickConnectAPI.InitiateQuickConnect(ctx).Execute(); err == nil {
		t.Fatal("expected InitiateQuickConnect to fail when disabled")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v", resp)
	}
}
