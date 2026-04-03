package gojenkins

import (
	"context"
	"strings"

	"golang.org/x/net/html"
)

// BuildParameter represents a parameter and its value used in a Jenkins build.
type BuildParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GetBuildParameters fetches the build's parameters page and scrapes the
// parameter names and values from the HTML response.
func (b *Build) GetBuildParameters(ctx context.Context) ([]BuildParameter, error) {
	var page string
	endpoint := b.Base + "/parameters/"
	_, err := b.Jenkins.Requester.Get(ctx, endpoint, &page, nil)
	if err != nil {
		return nil, err
	}
	return parseBuildParameters(page), nil
}

// parseBuildParameters parses the Jenkins build parameters HTML page and
// extracts parameter names and values.
//
// Handles three HTML structures:
//
// 1. String/Choice parameters (copy-button with text attribute)
// 2. Text parameters (<pre class="jenkins-readonly">)
// 3. Boolean/Checkbox parameters (<input type="checkbox"> + <label>)
func parseBuildParameters(page string) []BuildParameter {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}

	var params []BuildParameter
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "div" && nodeHasClass(n, "jenkins-form-item"):
				if p, ok := parseFormItem(n); ok {
					params = append(params, p)
				}
				return // don't recurse into children, already handled
			case n.Data == "span" && nodeHasClass(n, "jenkins-checkbox"):
				if p, ok := parseCheckbox(n); ok {
					params = append(params, p)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return params
}

// parseFormItem extracts name and value from a jenkins-form-item div.
// Handles string/choice params (copy-button) and text params (pre.jenkins-readonly).
func parseFormItem(n *html.Node) (BuildParameter, bool) {
	var p BuildParameter
	var findFields func(*html.Node)
	findFields = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch {
			case node.Data == "div" && nodeHasClass(node, "jenkins-form-label"):
				p.Name = textContent(node)
			case node.Data == "button" && nodeHasClass(node, "copy-button"):
				p.Value = nodeAttr(node, "text")
			case node.Data == "pre" && nodeHasClass(node, "jenkins-readonly"):
				p.Value = textContent(node)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			findFields(c)
		}
	}
	findFields(n)
	return p, p.Name != ""
}

// parseCheckbox extracts name and value from a jenkins-checkbox span.
func parseCheckbox(n *html.Node) (BuildParameter, bool) {
	var p BuildParameter
	var find func(*html.Node)
	find = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if node.Data == "input" && nodeAttr(node, "type") == "checkbox" {
				if nodeHasAttr(node, "checked") {
					p.Value = "true"
				} else {
					p.Value = "false"
				}
			}
			if node.Data == "label" {
				p.Name = textContent(node)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(n)
	return p, p.Name != ""
}

// nodeHasClass checks whether an HTML node has a specific CSS class.
func nodeHasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

// nodeHasAttr checks whether an HTML node has a specific attribute (regardless of value).
func nodeHasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// nodeAttr returns the value of a named attribute, or "" if not found.
func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textContent returns the concatenated text content of a node and its descendants.
func textContent(n *html.Node) string {
	var sb strings.Builder
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)
	return strings.TrimSpace(sb.String())
}
