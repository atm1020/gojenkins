package gojenkins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

type SearchResponse struct {
	Class       string        `json:"_class"`
	Suggestions []*Suggestion `json:"suggestions"`
}

type Suggestion struct {
	Group string `json:"group"`
	Icon  string `json:"icon"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	URL   string `json:"url"`
}

// GetURL returns the resource URL for this suggestion.
// Modern suggestions always have direct URLs.
func (s *Suggestion) GetURL() string {
	return s.URL
}

// LegacySearchResponse holds results from the legacy HTML search endpoint (Jenkins < 2.492).
type LegacySearchResponse struct {
	Suggestions []*LegacySuggestion
}

// LegacySuggestion represents a search result from legacy Jenkins (< 2.492).
// The URL is a partial search redirect (e.g. "?q=bab") that needs resolution.
type LegacySuggestion struct {
	Name string // from HTML anchor text
	URL  string // partial URL like "?q=bab" — needs resolution
}

// Resolve follows the 302 redirect to get the direct job URL.
// Returns a standard *Suggestion with Group="Items", Name, URL populated. Type will be "".
func (s *LegacySuggestion) Resolve(ctx context.Context, jenkins *Jenkins) (*Suggestion, error) {
	relURL, err := resolveBareName(ctx, s.Name, jenkins)
	if err != nil {
		return nil, err
	}
	name := s.Name
	if name == "" {
		name = lastPathSegment(relURL)
	}
	return &Suggestion{
		Group: "Items",
		Name:  name,
		URL:   relURL,
	}, nil
}

// parseSearchHTML parses the HTML response from /search/ and extracts legacy suggestions.
// Expected HTML structure:
//
//	<ol>
//	  <li id="item_name"><a href="?q=name">name</a></li>
//	</ol>
func parseSearchHTML(htmlContent io.Reader) ([]*LegacySuggestion, error) {
	var suggestions []*LegacySuggestion

	doc, err := html.Parse(htmlContent)
	if err != nil {
		return nil, err
	}

	// Find all <li> elements within <ol>
	var findListItems func(*html.Node)
	findListItems = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "li" {
			// Look for <a> tag within this <li>
			var findAnchor func(*html.Node)
			findAnchor = func(node *html.Node) {
				if node.Type == html.ElementNode && node.Data == "a" {
					// Extract href and text content
					var href, text string
					for _, attr := range node.Attr {
						if attr.Key == "href" {
							href = attr.Val
							break
						}
					}
					// Get text content of the anchor
					if node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
						text = strings.TrimSpace(node.FirstChild.Data)
					}

					// Create LegacySuggestion if both href and text are present.
					if href != "" && text != "" {
						suggestions = append(suggestions, &LegacySuggestion{
							Name: text,
							URL:  href,
						})
					}
				}

				for c := node.FirstChild; c != nil; c = c.NextSibling {
					findAnchor(c)
				}
			}
			findAnchor(n)
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findListItems(c)
		}
	}

	findListItems(doc)
	return suggestions, nil
}

// lastPathSegment returns the last non-empty segment of a URL path.
// e.g. "/job/bab/" → "bab", "/job/parent/job/child/" → "child"
func lastPathSegment(path string) string {
	trimmed := strings.TrimSuffix(path, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

// resolveBareName resolves a bare suggestion name (e.g. "bab") by calling the
// Jenkins search endpoint and expecting a 302 redirect to the job URL.
// Returns an error if the response status is not exactly 302.
func resolveBareName(ctx context.Context, name string, jenkins *Jenkins) (string, error) {
	// Build a non-redirecting client so we can inspect the 302 directly.
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Inherit the transport and TLS config from the configured requester.
	if r, ok := jenkins.Requester.(*Requester); ok && r.Client != nil {
		noRedirect.Transport = r.Client.Transport
	}

	// Build request URL: {server}/search/?q={name}&{crumb}
	reqURL := jenkins.Server + "/search/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("building search request: %w", err)
	}
	q := req.URL.Query()
	q.Set("q", name)
	for k, v := range jenkins.getCrumbQueryParams(ctx) {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	// Apply basic auth from the requester if present.
	if r, ok := jenkins.Requester.(*Requester); ok && r.BasicAuth != nil {
		req.SetBasicAuth(r.BasicAuth.Username, r.BasicAuth.Password)
	}

	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound { // 302
		return "", fmt.Errorf("expected 302 redirect from search, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("302 response missing Location header")
	}

	// Return relative URL (strip server prefix if present).
	return strings.TrimPrefix(location, jenkins.Server), nil
}
