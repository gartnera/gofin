package server_test

import (
	"fmt"
	"net/http"
	"testing"
)

// postAuthed issues a POST to path using the env's admin token (via ApiKey).
func postAuthed(t *testing.T, env *testEnv, path string) *http.Response {
	t.Helper()
	sep := "?"
	if len(path) > 0 && containsRune(path, '?') {
		sep = "&"
	}
	url := fmt.Sprintf("%s%s%sApiKey=%s", env.srv.URL, path, sep, env.token)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestRefreshLibrariesRequiresAuth(t *testing.T) {
	env := setupEnv(t)

	// No token -> 401.
	resp, err := http.Post(env.srv.URL+"/Library/Refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated refresh status = %d, want 401", resp.StatusCode)
	}

	// Admin token -> 204.
	resp = postAuthed(t, env, "/Library/Refresh")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin refresh status = %d, want 204", resp.StatusCode)
	}
}

func TestRefreshItem(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	id := firstMovie(t, client)

	resp := postAuthed(t, env, "/Items/"+id+"/Refresh")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("item refresh status = %d, want 204", resp.StatusCode)
	}

	// The item must still be resolvable after a refresh.
	if got := firstMovie(t, client); got != id {
		t.Fatalf("movie id after refresh = %q, want %q", got, id)
	}
}
