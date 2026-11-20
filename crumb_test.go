package gojenkins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Match the wire URL exactly: prefix-based fixtures hide duplicated api/json.
func TestAPITokenTriggerAndReplayCrumbPreflight(t *testing.T) {
	for _, basePath := range []string{"", "/jenkins"} {
		for _, crumbAvailable := range []bool{true, false} {
			name := "crumb"
			if !crumbAvailable {
				name = "html404"
			}
			t.Run(basePath+"/"+name, func(t *testing.T) {
				var posts, crumbs atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					username, token, ok := r.BasicAuth()
					if !ok || username != "fixture-user" || token != "fixture-api-token" {
						t.Error("API-token basic auth missing from request")
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}
					switch {
					case r.Method == http.MethodGet && r.URL.Path == basePath+"/crumbIssuer/api/json":
						crumbs.Add(1)
						if !crumbAvailable {
							w.Header().Set("Content-Type", "text/html")
							w.WriteHeader(http.StatusNotFound)
							_, _ = w.Write([]byte("<html><title>Not Found</title></html>"))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"crumbRequestField":"Jenkins-Crumb","crumb":"fixture-crumb"}`))
					case r.Method == http.MethodGet && r.URL.Path == basePath+"/job/demo/api/json":
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(JobResponse{Name: "demo", URL: "http://" + r.Host + basePath + "/job/demo/"})
					case r.Method == http.MethodPost && (r.URL.Path == basePath+"/job/demo/build" || r.URL.Path == basePath+"/job/demo/7/replay/run"):
						expectedCrumb := ""
						if crumbAvailable {
							expectedCrumb = "fixture-crumb"
						}
						if r.Header.Get("Jenkins-Crumb") != expectedCrumb {
							t.Error("incorrect crumb header")
							http.Error(w, "incorrect crumb", http.StatusForbidden)
							return
						}
						posts.Add(1)
						w.Header().Set("Location", basePath+"/queue/item/123/")
						w.WriteHeader(http.StatusCreated)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()

				jenkins := CreateJenkins(nil, server.URL+basePath, "fixture-user", "fixture-api-token")
				job := &Job{Jenkins: jenkins, Base: "/job/demo", Raw: &JobResponse{Name: "demo"}}
				t.Run("trigger", func(t *testing.T) {
					queueID, err := job.InvokeSimple(context.Background(), map[string]string{"delay": "0sec"})
					if err != nil || queueID != 123 {
						t.Fatalf("trigger = %d, %v; want queue 123", queueID, err)
					}
				})
				t.Run("replay", func(t *testing.T) {
					run := &PipelineRun{Job: job, Base: "/job/demo/7", ID: "7"}
					ok, err := run.Replay(context.Background(), map[string]string{"mainScript": "echo 'ok'"})
					if err != nil || !ok {
						t.Fatalf("replay = %t, %v; want success", ok, err)
					}
				})
				if crumbs.Load() != 2 || posts.Load() != 2 {
					t.Fatalf("received %d crumb requests and %d writes, want 2 each", crumbs.Load(), posts.Load())
				}
			})
		}
	}
}

func TestCrumbInvalidSuccessfulResponseStillBlocksPost(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><title>Sign in</title></html>"))
	}))
	defer server.Close()

	_, err := CreateJenkins(nil, server.URL).Requester.Post(context.Background(), "/job/demo/build", nil, nil, nil)
	if err == nil || posts.Load() != 0 {
		t.Fatalf("invalid HTTP 200 crumb response: err=%v, posts=%d; want error and no writes", err, posts.Load())
	}
}
