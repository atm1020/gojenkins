package gojenkins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

const staplerContentType = "application/x-stapler-method-invocation;charset=UTF-8"

// Parameter types, in the same vocabulary as ParameterDefinition.Type.
const (
	typeChoiceParameter           = "ChoiceParameter"
	typeCascadeChoiceParameter    = "CascadeChoiceParameter"
	typeDynamicReferenceParameter = "DynamicReferenceParameter"
	typeChoiceParameterDefinition = "ChoiceParameterDefinition"
)

// ErrUnsupportedForm means the build form yielded no parameters this package
// can parse. It is the fail-soft signal: callers should fall back to
// Job.GetParameters and plain text inputs rather than block the build.
var ErrUnsupportedForm = errors.New("unsupported Jenkins build form")

// ErrNoChoices means a cascading parameter resolved to no choices at all.
// The usual cause is a Groovy script that is not approved or not sandboxed:
// uno-choice swallows the UnapprovedUsageException server-side and answers
// 200 with [[],[]]. Report it rather than rendering an empty dropdown.
var ErrNoChoices = errors.New("parameter resolved to no choices (script not approved or returned nothing)")

// ErrStaleBuildForm means a bound proxy answered 404: its uuid belongs to a
// render or session that no longer exists. The BuildForm is unusable; obtain
// a fresh one with Job.GetBuildForm.
var ErrStaleBuildForm = errors.New("build form is stale; render it again")

// Choice is one displayed and submitted option in a Jenkins build form.
type Choice struct {
	Value    string
	Label    string
	Selected bool
	Disabled bool
}

// FormParameter is a build parameter as the form renders it, which is a strict
// superset of what ParameterDefinition can describe.
type FormParameter struct {
	Name string
	Type string // same vocabulary as ParameterDefinition.Type

	// Choices are the options to offer. They are nil for a parameter that is
	// not a choice, and for a cascading parameter until it is resolved.
	Choices []Choice

	// ReferencedParameters names the parameters this one is computed from.
	// Non-empty only for CascadeChoiceParameter / DynamicReferenceParameter,
	// and exactly the set that must be supplied to Resolve.
	ReferencedParameters []string

	// OmitValueField is set for a DynamicReferenceParameter whose form has no
	// value field: uno-choice's omitValueField option leaves out the hidden
	// "value" input, so the browser submits nothing for it. Callers should
	// leave such a parameter out of the build request.
	OmitValueField bool

	proxy   string // "/$stapler/bound/<uuid>"; empty unless bound
	methods []string
}

// formSession is the HTTP session one form render created. The bound proxies
// on that render only answer requests that replay it.
type formSession struct {
	cookie      string // verbatim "name=value" from the render's Set-Cookie
	crumb       string
	crumbHeader string
}

// BuildForm is one render of a job's parameterized build form.
//
// Active Choices (uno-choice) exposes nothing through the REST API: choice lists
// live only in the HTML build form, and cascading choices are computed by a
// server-side object bound to the render. A BuildForm therefore owns the HTTP
// session that render created and is valid only for that session's lifetime.
// It is one build dialog's session, not shared state: obtain a fresh one each
// time a dialog is opened, do not cache it, and do not call Resolve on it from
// more than one goroutine at a time.
type BuildForm struct {
	Parameters []FormParameter

	job     *Job
	session formSession
}

// GetBuildForm renders the job's build form and parses every parameter from it.
// Static and plain Active Choices parameters come back with Choices already
// populated; cascading ones come back with Choices nil and ReferencedParameters
// set, and must be resolved with Resolve.
func (j *Job) GetBuildForm(ctx context.Context) (*BuildForm, error) {
	ar := NewAPIRequest(http.MethodGet, j.Base+"/build", nil)
	ar.Suffix = ""
	var page string
	resp, err := j.Jenkins.Requester.Do(ctx, ar, &page, map[string]string{"delay": "0sec"})
	if err != nil {
		return nil, fmt.Errorf("render build form: %w", err)
	}
	if resp == nil {
		return nil, errors.New("render build form: no response")
	}
	// Jenkins renders the form with a 405 because the page is meant to be
	// POSTed back; the body is the complete form all the same.
	if !isSuccess(resp.StatusCode) && resp.StatusCode != http.StatusMethodNotAllowed {
		return nil, fmt.Errorf("render build form: HTTP %d", resp.StatusCode)
	}

	form, err := parseBuildForm(page)
	if err != nil {
		return nil, err
	}
	form.job = j
	form.session.cookie = firstCookie(resp.Header)
	return form, nil
}

// Dependents returns the names of parameters that reference name, in form
// declaration order, so a caller can cascade a change without walking
// Parameters itself. Callers must guard against cycles: the dependency graph
// can chain (A -> B -> C), and the plugin permits, though does not encourage,
// cycles.
func (f *BuildForm) Dependents(name string) []string {
	var result []string
	for _, p := range f.Parameters {
		if slices.Contains(p.ReferencedParameters, name) {
			result = append(result, p.Name)
		}
	}
	return result
}

// Resolve recomputes the choices for one cascading parameter given the current
// values of the parameters it references. values must cover
// FormParameter.ReferencedParameters; extra entries are ignored by Jenkins.
//
// On success the matching FormParameter.Choices is replaced with the returned
// choices. It returns ErrNoChoices when the script yields nothing,
// ErrStaleBuildForm when the render's binding has expired, and an error for
// a DynamicReferenceParameter, whose markup has no []Choice form.
func (f *BuildForm) Resolve(ctx context.Context, name string, values map[string]string) ([]Choice, error) {
	i := slices.IndexFunc(f.Parameters, func(p FormParameter) bool { return p.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("resolve %q: no such parameter on the form", name)
	}
	parameter := f.Parameters[i]
	if parameter.Type == typeDynamicReferenceParameter {
		return nil, fmt.Errorf("resolve %q: unsupported parameter type %s: it renders markup, not choices", name, parameter.Type)
	}
	if parameter.proxy == "" || !slices.Contains(parameter.methods, "getChoicesForUI") {
		return nil, fmt.Errorf("resolve %q: parameter has no bound choice resolver", name)
	}
	if f.session.crumb == "" || f.session.crumbHeader == "" {
		return nil, fmt.Errorf("resolve %q: build form carried no crumb, so the bound proxy would answer 403", name)
	}

	if slices.Contains(parameter.methods, "doUpdate") {
		parents := make([]string, 0, len(parameter.ReferencedParameters))
		for _, parent := range parameter.ReferencedParameters {
			parents = append(parents, parent+"="+values[parent])
		}
		body, err := json.Marshal([]string{strings.Join(parents, "__LESEP__")})
		if err != nil {
			return nil, fmt.Errorf("encode doUpdate arguments: %w", err)
		}
		if err := f.invoke(ctx, parameter.proxy+"/doUpdate", body, nil); err != nil {
			return nil, err
		}
	}

	var raw [][]string
	if err := f.invoke(ctx, parameter.proxy+"/getChoicesForUI", []byte("[]"), &raw); err != nil {
		return nil, err
	}
	if len(raw) != 2 {
		return nil, fmt.Errorf("resolve %q: invalid choice response", name)
	}
	choices := pairChoices(raw[0], raw[1])
	if len(choices) == 0 {
		return nil, fmt.Errorf("resolve %q: %w", name, ErrNoChoices)
	}
	f.Parameters[i].Choices = choices
	return choices, nil
}

// invoke calls one method on a bound proxy. A 404 is reported as
// ErrStaleBuildForm.
//
// This deliberately goes through Requester.Do and not Requester.Post, and
// sets the crumb itself rather than calling SetCrumb. It is the exception to
// "CSRF crumb handling is automatic": SetCrumb fetches a fresh crumb, which
// replaces the Cookie with the crumb response's own session, and the bound
// uuid belongs to the render's session, so the call would 404. Post also
// hardcodes a form-urlencoded Content-Type, which Stapler does not route.
func (f *BuildForm) invoke(ctx context.Context, endpoint string, body []byte, target interface{}) error {
	ar := NewAPIRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	ar.SetHeader("Content-Type", staplerContentType)     // else 404: not routed to the method
	ar.SetHeader("Cookie", f.session.cookie)             // else 404: stale bind
	ar.SetHeader("Crumb", f.session.crumb)               // else 403
	ar.SetHeader(f.session.crumbHeader, f.session.crumb) // else 403
	// Read the body raw: Requester.Do decodes JSON before the status is
	// visible, and Jenkins answers errors with an HTML page.
	var raw string
	resp, err := f.job.Jenkins.Requester.Do(ctx, ar, &raw)
	if err != nil {
		return fmt.Errorf("invoke %s: %w", endpoint, err)
	}
	if resp == nil {
		return fmt.Errorf("invoke %s: no response", endpoint)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("invoke %s: %w", endpoint, ErrStaleBuildForm)
	}
	if !isSuccess(resp.StatusCode) {
		return fmt.Errorf("invoke %s: HTTP %d", endpoint, resp.StatusCode)
	}
	if target == nil || raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("invoke %s: decode response: %w", endpoint, err)
	}
	return nil
}

// parsedParameter is a FormParameter plus what the parser learned about it
// before its type is decided.
type parsedParameter struct {
	FormParameter
	activeChoice bool
	hasSelect    bool
	hasValue     bool   // the body has a form control named "value"
	holderType   string // parameter type implied by its data-holder span, if any
}

// dataHolder is the metadata uno-choice renders on a
// cascade-choice-parameter-data-holder or
// dynamic-reference-parameter-data-holder span.
type dataHolder struct {
	name       string
	referenced []string
	proxyName  string
	paramType  string
}

func readDataHolder(n *html.Node) (dataHolder, bool) {
	if n.Type != html.ElementNode || n.Data != "span" {
		return dataHolder{}, false
	}
	var paramType string
	switch {
	case nodeHasClass(n, "cascade-choice-parameter-data-holder"):
		paramType = typeCascadeChoiceParameter
	case nodeHasClass(n, "dynamic-reference-parameter-data-holder"):
		paramType = typeDynamicReferenceParameter
	default:
		return dataHolder{}, false
	}
	return dataHolder{
		name:       nodeAttr(n, "data-name"),
		referenced: splitNames(nodeAttr(n, "data-referenced-parameters")),
		proxyName:  nodeAttr(n, "data-proxy-name"),
		paramType:  paramType,
	}, true
}

func (p *parsedParameter) applyDataHolder(h dataHolder) {
	p.Name = h.name
	p.ReferencedParameters = h.referenced
	p.holderType = h.paramType
}

// decideType is the one place a parameter's type is decided.
func (p *parsedParameter) decideType() {
	switch {
	case p.holderType != "":
		p.Type = p.holderType
		p.Choices = nil // server-rendered options on a bound parameter are fallback values
		p.OmitValueField = p.Type == typeDynamicReferenceParameter && !p.hasValue
	case p.activeChoice:
		p.Type = typeChoiceParameter
	case p.hasSelect:
		p.Type = typeChoiceParameterDefinition
	}
}

func parseBuildForm(page string) (*BuildForm, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedForm, err)
	}
	form := &BuildForm{}
	var parameters []parsedParameter
	var holders []dataHolder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "head" {
				form.session.crumb = nodeAttr(n, "data-crumb-value")
				form.session.crumbHeader = nodeAttr(n, "data-crumb-header")
			}
			if n.Data == "div" && nodeAttr(n, "name") == "parameter" {
				if p, ok := parseParameterBody(n); ok {
					parameters = append(parameters, p)
				}
				return
			}
			if h, ok := readDataHolder(n); ok {
				holders = append(holders, h)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	for _, h := range holders {
		for i := range parameters {
			if parameters[i].Name != h.name {
				continue
			}
			parameters[i].applyDataHolder(h)
			if proxy, methods := findBoundScript(doc, h.proxyName); proxy != "" {
				parameters[i].proxy, parameters[i].methods = proxy, methods
			}
		}
	}
	for _, p := range parameters {
		p.decideType()
		form.Parameters = append(form.Parameters, p.FormParameter)
	}
	if len(form.Parameters) == 0 {
		return nil, ErrUnsupportedForm
	}
	return form, nil
}

// findBoundScript finds the bound-proxy script for proxyName, or the first
// bound-proxy script in the page when proxyName is empty.
func findBoundScript(doc *html.Node, proxyName string) (string, []string) {
	var proxy string
	var methods []string
	var find func(*html.Node) bool
	find = func(node *html.Node) bool {
		if node.Type == html.ElementNode && node.Data == "script" {
			src := nodeAttr(node, "src")
			if proxyName != "" {
				u, err := url.Parse(src)
				if err != nil || u.Query().Get("var") != proxyName {
					return false
				}
			}
			if proxy, methods = parseBoundScript(src); proxy != "" {
				return true
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if find(child) {
				return true
			}
		}
		return false
	}
	find(doc)
	return proxy, methods
}

func parseParameterBody(root *html.Node) (parsedParameter, bool) {
	var p parsedParameter
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if h, ok := readDataHolder(n); ok {
				p.applyDataHolder(h)
			}
			switch {
			case n.Data == "div" && nodeAttr(n, "name") == "parameter":
				if nodeHasClass(n, "active-choice") {
					p.activeChoice = true
				}
			case n.Data == "input" && nodeAttr(n, "name") == "name" && p.Name == "":
				p.Name = nodeAttr(n, "value")
			case n.Data == "select" && nodeAttr(n, "name") == "value":
				p.hasSelect = true
				p.hasValue = true
			case (n.Data == "input" || n.Data == "textarea") && nodeAttr(n, "name") == "value":
				p.hasValue = true
			case n.Data == "option":
				p.Choices = append(p.Choices, Choice{Value: nodeAttr(n, "value"), Label: textContent(n), Selected: nodeHasAttr(n, "selected"), Disabled: nodeHasAttr(n, "disabled")})
			case n.Data == "script":
				if proxy, methods := parseBoundScript(nodeAttr(n, "src")); proxy != "" {
					p.proxy, p.methods = proxy, methods
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if p.Name == "" {
		return parsedParameter{}, false
	}
	return p, true
}

func parseBoundScript(src string) (string, []string) {
	if !strings.Contains(src, "/$stapler/bound/script/$stapler/bound/") {
		return "", nil
	}
	u, err := url.Parse(src)
	if err != nil {
		return "", nil
	}
	proxy := strings.Replace(u.Path, "/$stapler/bound/script", "", 1)
	return proxy, splitNames(u.Query().Get("methods"))
}

func pairChoices(labels, values []string) []Choice {
	count := min(len(labels), len(values))
	choices := make([]Choice, 0, count)
	for i := 0; i < count; i++ {
		// Markers belong to the display label. The value is submitted to
		// buildWithParameters, so it is kept exactly as Jenkins sent it.
		label, selected, disabled := stripChoiceMarkers(labels[i])
		choices = append(choices, Choice{Value: values[i], Label: label, Selected: selected, Disabled: disabled})
	}
	return choices
}

func stripChoiceMarkers(value string) (string, bool, bool) {
	selected, disabled := false, false
	for {
		switch {
		case strings.HasSuffix(value, ":selected"):
			selected = true
			value = strings.TrimSuffix(value, ":selected")
		case strings.HasSuffix(value, ":disabled"):
			disabled = true
			value = strings.TrimSuffix(value, ":disabled")
		default:
			return value, selected, disabled
		}
	}
}

func isSuccess(status int) bool {
	return status >= 200 && status < 300
}

func firstCookie(header http.Header) string {
	if header == nil {
		return ""
	}
	cookie := header.Get("Set-Cookie")
	if before, _, ok := strings.Cut(cookie, ";"); ok {
		return before
	}
	return cookie
}

func splitNames(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
