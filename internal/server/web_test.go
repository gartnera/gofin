package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/server"
)

// setupWebEnv builds a tiny in-memory server with a temp web directory wired
// up via WithWebRoot. It mirrors setupEnv but skips the media scan so the
// tests focus on static-file routing.
func setupWebEnv(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	// Mixed-case filename: the static handler must not lowercase paths under
	// /web/ — chunk filenames in the real bundle are case-sensitive.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "MainBundle.js"), []byte("/*js*/"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	srv := httptest.NewServer(server.New(client, "test", server.WithWebRoot(root)))
	t.Cleanup(srv.Close)
	return srv, root
}

func TestRootRedirectsToWeb(t *testing.T) {
	srv, _ := setupWebEnv(t)

	// Disable following the redirect so we can inspect the response itself.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/web/" {
		t.Errorf("Location = %q, want /web/", loc)
	}
}

func TestWebServesIndexAndPreservesCase(t *testing.T) {
	srv, _ := setupWebEnv(t)

	// /web/ should serve index.html.
	resp, err := http.Get(srv.URL + "/web/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/web/ status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "<html>ok</html>" {
		t.Errorf("body = %q, want index.html contents", body)
	}

	// A mixed-case file must be reachable verbatim (path normalisation
	// would lowercase MainBundle.js to mainbundle.js and miss the file).
	resp2, err := http.Get(srv.URL + "/web/MainBundle.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("/web/MainBundle.js status = %d, want 200", resp2.StatusCode)
	}
}

func TestWebNotEnabledWhenWebRootUnset(t *testing.T) {
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	srv := httptest.NewServer(server.New(client, "test"))
	t.Cleanup(srv.Close)

	// Without WebRoot, both / and /web/ should 404 — gofin still answers
	// API requests but doesn't host a client.
	for _, p := range []string{"/", "/web/", "/web/index.html"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", p, resp.StatusCode)
		}
	}
}
