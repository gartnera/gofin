package server

import (
	"net/http/httptest"
	"testing"
)

func TestParseAuthorization(t *testing.T) {
	header := `MediaBrowser Client="Android TV", Device="Shield", DeviceId="abc123", Version="1.2.3", Token="deadbeef"`
	pairs := ParseAuthorization(header)
	want := map[string]string{
		"Client":   "Android TV",
		"Device":   "Shield",
		"DeviceId": "abc123",
		"Version":  "1.2.3",
		"Token":    "deadbeef",
	}
	for k, v := range want {
		if pairs[k] != v {
			t.Errorf("pair %q = %q, want %q", k, pairs[k], v)
		}
	}
}

func TestParseAuthorizationEmpty(t *testing.T) {
	if len(ParseAuthorization("")) != 0 {
		t.Error("expected empty map for empty header")
	}
}

func TestTokenFromRequest(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(r *httptest.ResponseRecorder)
		header map[string]string
		query  string
		want   string
	}{
		{name: "authorization header", header: map[string]string{"Authorization": `MediaBrowser Token="tok1"`}, want: "tok1"},
		{name: "emby authorization", header: map[string]string{"X-Emby-Authorization": `MediaBrowser Token="tok2"`}, want: "tok2"},
		{name: "emby token header", header: map[string]string{"X-Emby-Token": "tok3"}, want: "tok3"},
		{name: "mediabrowser token header", header: map[string]string{"X-MediaBrowser-Token": "tok4"}, want: "tok4"},
		{name: "api key query", query: "ApiKey=tok5", want: "tok5"},
		{name: "lowercase api_key query", query: "api_key=tok6", want: "tok6"},
		{name: "missing", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/Items?"+tt.query, nil)
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			if got := tokenFromRequest(r); got != tt.want {
				t.Errorf("tokenFromRequest = %q, want %q", got, tt.want)
			}
		})
	}
}
