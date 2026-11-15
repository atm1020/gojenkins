package gojenkins

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		wantTruncated           bool
	}{
		{"small", "text/html", "<!doctype html><html><body>diagnostic fixture</body></html>", false},
		{"decoder buffer boundary", "text/html", "<html>" + strings.Repeat("x", 1500) + "</html>", false},
		{"limit", "text/html", "<html>" + strings.Repeat("x", maxErrorBodyBytes-6), false},
		{"large", "text/html", "<html>" + strings.Repeat("x", maxErrorBodyBytes) + "</html>", true},
		{"mislabelled", "application/json", "<html>diagnostic fixture</html>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var result map[string]interface{}
			_, err := CreateJenkins(nil, srv.URL).Requester.GetJSON(context.Background(), "/api/json", &result, nil)
			var decodeErr *ResponseDecodeError
			if !errors.As(err, &decodeErr) {
				t.Fatalf("error = %v", err)
			}
			body, truncated := decodeErr.HTMLResponseBody()
			want := tc.body
			if len(want) > maxErrorBodyBytes {
				want = want[:maxErrorBodyBytes]
			}
			if body != want || truncated != tc.wantTruncated {
				t.Fatal("HTML capture differs from original bytes or truncation flag")
			}
			if strings.Contains(fmt.Sprint(err), "<html>") {
				t.Fatal("HTML leaked into error message")
			}
		})
	}
}
