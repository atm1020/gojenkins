package gojenkins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- GlobalSearch (modern, >= 2.492) ---

func TestJenkins_GlobalSearch_Success(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		if sr, ok := response.(*SearchResponse); ok {
			sr.Class = "hudson.search.Search$Result"
			sr.Suggestions = []*Suggestion{
				{Name: "my-pipeline", Type: "hudson.model.FreeStyleProject", URL: "/job/my-pipeline/"},
				{Name: "my-folder/my-job", Type: "hudson.model.FreeStyleProject", URL: "/job/my-folder/job/my-job/"},
			}
		}
		return &http.Response{StatusCode: 200}, nil
	}

	result, err := jenkins.GlobalSearch(context.Background(), "my")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 2, len(result.Suggestions))
	assert.Equal(t, "my-pipeline", result.Suggestions[0].Name)
	assert.Equal(t, "my-folder/my-job", result.Suggestions[1].Name)
}

func TestJenkins_GlobalSearch_NoResults(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{StatusCode: 200}, nil
	}

	result, err := jenkins.GlobalSearch(context.Background(), "nonexistent")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, len(result.Suggestions))
}

func TestJenkins_GlobalSearch_PassesQuery(t *testing.T) {
	jenkins := newMockJenkins()
	var capturedQuery map[string]string
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		capturedQuery = query
		return &http.Response{StatusCode: 200}, nil
	}

	_, err := jenkins.GlobalSearch(context.Background(), "pipeline")
	assert.NoError(t, err)
	assert.Equal(t, "pipeline", capturedQuery["query"])
}

func TestJenkins_GlobalSearch_UsesCorrectEndpoint(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		assert.Equal(t, "/search/suggest", endpoint)
		return &http.Response{StatusCode: 200}, nil
	}

	_, _ = jenkins.GlobalSearch(context.Background(), "test")
}

func TestJenkins_GlobalSearch_RequesterError(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).err = assert.AnError

	result, err := jenkins.GlobalSearch(context.Background(), "my")
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestJenkins_GlobalSearch_NonOKStatus(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{StatusCode: 500}, nil
	}

	result, err := jenkins.GlobalSearch(context.Background(), "my")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, "500", err.Error())
}

func TestJenkins_GlobalSearch_ErrorOnOldVersion(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Version = "2.400"

	result, err := jenkins.GlobalSearch(context.Background(), "test")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "use GlobalSearchLegacy()")
}

// --- Legacy search (GlobalSearchLegacy, Jenkins < 2.492) ---

func TestGlobalSearchLegacy_ParsesHTML(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Version = "2.400"

	htmlBody := `<html><body><ol>
		<li><a href="?q=my-job">my-job</a></li>
		<li><a href="?q=other-job">other-job</a></li>
	</ol></body></html>`

	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(htmlBody)),
		}, nil
	}

	result, err := jenkins.GlobalSearchLegacy(context.Background(), "my")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 2, len(result.Suggestions))
	assert.Equal(t, "my-job", result.Suggestions[0].Name)
	assert.Equal(t, "?q=my-job", result.Suggestions[0].URL)
	assert.Equal(t, "other-job", result.Suggestions[1].Name)
}

func TestGlobalSearchLegacy_CrumbPassedAsQueryParam(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Version = "2.400"

	jenkins.Requester.(*MockRequester).GetJSONFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		if endpoint == "/crumbIssuer/api/json" {
			if m, ok := response.(*map[string]string); ok {
				*m = map[string]string{
					"crumbRequestField": "Jenkins-Crumb",
					"crumb":             "abc123",
				}
			}
			return &http.Response{StatusCode: 200}, nil
		}
		return &http.Response{StatusCode: 200}, nil
	}

	var capturedQuery map[string]string
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		capturedQuery = query
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader("<ol></ol>")),
		}, nil
	}

	_, err := jenkins.GlobalSearchLegacy(context.Background(), "test")
	assert.NoError(t, err)
	assert.Equal(t, "abc123", capturedQuery["Jenkins-Crumb"])
}

func TestGlobalSearchLegacy_SingleResult302(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Version = "2.400"

	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("")),
			Request: &http.Request{
				URL: &url.URL{
					Scheme: "http",
					Host:   "localhost:8080",
					Path:   "/job/my-job/",
				},
			},
		}, nil
	}

	result, err := jenkins.GlobalSearchLegacy(context.Background(), "my-job")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 1, len(result.Suggestions))
	assert.Equal(t, "my-job", result.Suggestions[0].Name)
	assert.Contains(t, result.Suggestions[0].URL, "/job/my-job/")
}

// --- Suggestion.GetURL ---

func TestSuggestion_GetURL_DirectURL(t *testing.T) {
	s := &Suggestion{Name: "foo", URL: "/job/foo/"}
	gotURL := s.GetURL()
	assert.Equal(t, "/job/foo/", gotURL)
}

func TestSuggestion_GetURL_Empty(t *testing.T) {
	s := &Suggestion{Name: "foo"}
	gotURL := s.GetURL()
	assert.Equal(t, "", gotURL)
}

// --- parseSearchHTML ---

func TestParseSearchHTML_NormalizesURL(t *testing.T) {
	html := `<html><body><ol>
		<li><a href="?q=bab">bab</a></li>
		<li><a href="?q=other">other</a></li>
	</ol></body></html>`

	suggestions, err := parseSearchHTML(strings.NewReader(html))
	assert.NoError(t, err)
	assert.Equal(t, 2, len(suggestions))
	assert.Equal(t, "?q=bab", suggestions[0].URL)
	assert.Equal(t, "bab", suggestions[0].Name)
	assert.Equal(t, "?q=other", suggestions[1].URL)
}

// --- LegacySuggestion.Resolve ---

func TestLegacySuggestion_Resolve(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/job/bab/", http.StatusFound)
	}))
	defer server.Close()

	jenkins := newMockJenkins()
	jenkins.Server = server.URL

	s := &LegacySuggestion{Name: "bab", URL: "?q=bab"}
	got, err := s.Resolve(context.Background(), jenkins)
	assert.NoError(t, err)
	assert.Equal(t, "Items", got.Group)
	assert.Equal(t, "bab", got.Name)
	assert.Equal(t, "/job/bab/", got.URL)
}

func TestLegacySuggestion_Resolve_PreservesHTMLName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/job/folder/job/child/", http.StatusFound)
	}))
	defer server.Close()

	jenkins := newMockJenkins()
	jenkins.Server = server.URL

	s := &LegacySuggestion{Name: "folder » child", URL: "?q=child"}
	got, err := s.Resolve(context.Background(), jenkins)
	assert.NoError(t, err)
	assert.Equal(t, "folder » child", got.Name)
	assert.Equal(t, "/job/folder/job/child/", got.URL)
}

func TestLegacySuggestion_Resolve_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	jenkins := newMockJenkins()
	jenkins.Server = server.URL

	s := &LegacySuggestion{Name: "bab", URL: "?q=bab"}
	got, err := s.Resolve(context.Background(), jenkins)
	assert.Error(t, err)
	assert.Nil(t, got)
}

// --- IsVersionGreaterOrEqual ---

func TestIsVersionGreaterOrEqual(t *testing.T) {
	jenkins := newMockJenkins()

	jenkins.Version = "2.492"
	assert.True(t, jenkins.IsVersionGreaterOrEqual("2.492"))

	jenkins.Version = "2.500"
	assert.True(t, jenkins.IsVersionGreaterOrEqual("2.492"))

	jenkins.Version = "2.400"
	assert.False(t, jenkins.IsVersionGreaterOrEqual("2.492"))

	jenkins.Version = ""
	assert.True(t, jenkins.IsVersionGreaterOrEqual("2.492"))
}
