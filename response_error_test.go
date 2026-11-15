package gojenkins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequesterJSONErrorContext(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{"server HTML", 500, "text/html; charset=utf-8", "<html>private-token-secret</html>", "HTML instead of JSON"},
		{"login HTML", 200, "text/html", "<html>private-token-secret</html>", "HTML instead of JSON"},
		{"invalid JSON", 502, "application/json", "{broken", "invalid JSON"},
		{"XHTML", 403, "application/xhtml+xml", "<html>private-token-secret</html>", "HTML instead of JSON"},
		{"unlabelled HTML", 500, "application/octet-stream", "<html>private-token-secret</html>", "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var result map[string]interface{}
			resp, err := CreateJenkins(nil, srv.URL).Requester.GetJSON(context.Background(), "/api/json", &result, nil)
			if err == nil {
				t.Fatal("expected a response decoding error")
			}
			if !strings.Contains(err.Error(), http.StatusText(tc.status)) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error lacks response context: %v", err)
			}
			if strings.Contains(err.Error(), "private-token-secret") || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error exposes response body or private URL: %v", err)
			}
			var responseErr *ResponseDecodeError
			if !errors.As(err, &responseErr) || responseErr.StatusCode != tc.status {
				t.Fatalf("missing typed response context: %v", err)
			}
			if resp == nil || resp.StatusCode != tc.status {
				t.Fatal("HTTP response was discarded")
			}
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) {
				t.Fatalf("JSON error is not preserved: %v", err)
			}
		})
	}
}
