package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	jfapi "github.com/sj14/jellyfin-go/api"
)

// postJSONAuthed issues a POST with a JSON body using the env's admin token.
func postJSONAuthed(t *testing.T, env *testEnv, path, body string) *http.Response {
	t.Helper()
	sep := "?"
	if containsRune(path, '?') {
		sep = "&"
	}
	url := env.srv.URL + path + sep + "ApiKey=" + env.token
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUpdateItemMetadata(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()
	id := firstMovie(t, client)

	body := `{"Name":"Inception (Edited)","Overview":"A thief steals secrets.","ProductionYear":2011}`
	resp := postJSONAuthed(t, env, "/Items/"+id, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update status = %d, want 204", resp.StatusCode)
	}

	got, _, err := client.UserLibraryAPI.GetItem(ctx, id).Execute()
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.GetName() != "Inception (Edited)" {
		t.Errorf("Name = %q, want %q", got.GetName(), "Inception (Edited)")
	}
	if got.GetOverview() != "A thief steals secrets." {
		t.Errorf("Overview = %q, want set", got.GetOverview())
	}
	if got.GetProductionYear() != 2011 {
		t.Errorf("ProductionYear = %d, want 2011", got.GetProductionYear())
	}
}

func TestUpdateItemRejectsEmptyName(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	id := firstMovie(t, client)

	resp := postJSONAuthed(t, env, "/Items/"+id, `{"Name":"   "}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank-name update status = %d, want 400", resp.StatusCode)
	}
}

func TestUpdateItemRequiresAdmin(t *testing.T) {
	env := setupEnv(t)

	// No token -> 401.
	resp, err := http.Post(env.srv.URL+"/Items/deadbeef", "application/json", strings.NewReader(`{"Name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update status = %d, want 401", resp.StatusCode)
	}
}

// TestLockedMetadataSurvivesRefresh proves the Jellyfin LockData behaviour:
// an edit with LockData=true is preserved when the item is re-scanned, whereas
// an unlocked edit is re-derived from the filename.
func TestLockedMetadataSurvivesRefresh(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()
	id := firstMovie(t, client)

	// Locked edit: rename and lock the whole item.
	locked := postJSONAuthed(t, env, "/Items/"+id, `{"Name":"Custom Title","LockData":true}`)
	locked.Body.Close()
	if locked.StatusCode != http.StatusNoContent {
		t.Fatalf("locked update status = %d, want 204", locked.StatusCode)
	}

	// Refresh re-derives metadata from the file; the lock must protect the edit.
	refresh := postAuthed(t, env, "/Items/"+id+"/Refresh")
	refresh.Body.Close()
	if refresh.StatusCode != http.StatusNoContent {
		t.Fatalf("refresh status = %d, want 204", refresh.StatusCode)
	}

	got, _, err := client.UserLibraryAPI.GetItem(ctx, id).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if got.GetName() != "Custom Title" {
		t.Errorf("locked name after refresh = %q, want %q", got.GetName(), "Custom Title")
	}
	if !got.GetLockData() {
		t.Error("LockData not surfaced after edit")
	}

	// Now unlock and rename again: the next refresh should overwrite it.
	unlocked := postJSONAuthed(t, env, "/Items/"+id, `{"Name":"Will Be Lost","LockData":false}`)
	unlocked.Body.Close()
	if unlocked.StatusCode != http.StatusNoContent {
		t.Fatalf("unlocked update status = %d, want 204", unlocked.StatusCode)
	}
	refresh2 := postAuthed(t, env, "/Items/"+id+"/Refresh")
	refresh2.Body.Close()

	got2, _, err := client.UserLibraryAPI.GetItem(ctx, id).Execute()
	if err != nil {
		t.Fatal(err)
	}
	// Filename is "Inception (2010).mp4" -> parsed title "Inception".
	if got2.GetName() != "Inception" {
		t.Errorf("unlocked name after refresh = %q, want re-derived %q", got2.GetName(), "Inception")
	}
}

// TestPerFieldLockSurvivesRefresh checks that a single locked field (Name) is
// preserved while unlocked fields still refresh from the file.
func TestPerFieldLockSurvivesRefresh(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()
	id := firstMovie(t, client)

	body := `{"Name":"Name Locked","LockedFields":["Name"]}`
	resp := postJSONAuthed(t, env, "/Items/"+id, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update status = %d, want 204", resp.StatusCode)
	}

	refresh := postAuthed(t, env, "/Items/"+id+"/Refresh")
	refresh.Body.Close()

	got, _, err := client.UserLibraryAPI.GetItem(ctx, id).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if got.GetName() != "Name Locked" {
		t.Errorf("per-field-locked name after refresh = %q, want %q", got.GetName(), "Name Locked")
	}
	if fields := got.GetLockedFields(); len(fields) != 1 || fields[0] != jfapi.METADATAFIELD_NAME {
		t.Errorf("LockedFields = %v, want [Name]", got.GetLockedFields())
	}
}
