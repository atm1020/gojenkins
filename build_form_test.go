package gojenkins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureJobBase = "/job/Ops/job/Deployments/job/dynamic-deploy-active-choices"
	fixtureProxy   = "/$stapler/bound/dc2c1d76-ac9a-4b31-8ef8-596c562cfe94"
	fixtureCookie  = "JSESSIONID.7c61d0ce=node017nnl0fixture"
	fixtureCrumb   = "fixture-crumb"
)

// staplerReply is one canned answer from fakeStapler.
type staplerReply struct {
	status int
	body   string
}

// staplerCall records one request that reached fakeStapler.
type staplerCall struct {
	endpoint string
	headers  http.Header
	body     string
}

// fakeStapler answers MockRequester.Do the way Requester.Do would: it returns
// the response for any status, fills a *string with the raw body, and decodes
// JSON into anything else.
type fakeStapler struct {
	routes map[string]staplerReply
	calls  []staplerCall
}

func (s *fakeStapler) do(_ context.Context, ar *APIRequest, response interface{}, _ ...interface{}) (*http.Response, error) {
	var body string
	if ar.Payload != nil {
		raw, err := io.ReadAll(ar.Payload)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	s.calls = append(s.calls, staplerCall{endpoint: ar.Endpoint, headers: ar.Headers.Clone(), body: body})

	reply, ok := s.routes[ar.Endpoint]
	if !ok {
		reply = staplerReply{status: http.StatusNotFound}
	}
	resp := &http.Response{StatusCode: reply.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply.body))}
	if ar.Endpoint == fixtureJobBase+"/build" {
		resp.Header.Set("Set-Cookie", fixtureCookie+"; Path=/; HttpOnly; SameSite=Lax")
	}
	switch target := response.(type) {
	case nil:
	case *string:
		*target = reply.body
	default:
		if reply.body != "" {
			if err := json.Unmarshal([]byte(reply.body), target); err != nil {
				return nil, err
			}
		}
	}
	return resp, nil
}

func (s *fakeStapler) callsTo(endpoint string) []staplerCall {
	var result []staplerCall
	for _, c := range s.calls {
		if c.endpoint == endpoint {
			result = append(result, c)
		}
	}
	return result
}

func loadBuildFormFixture(t *testing.T) string {
	t.Helper()
	page, err := os.ReadFile("_tests/build_form.html")
	require.NoError(t, err)
	return string(page)
}

// newFakeStapler serves page as the build form with a 405, as Jenkins does,
// and answers the SERVER proxy's two methods.
func newFakeStapler(page string, choices string) *fakeStapler {
	return &fakeStapler{routes: map[string]staplerReply{
		fixtureJobBase + "/build":         {status: http.StatusMethodNotAllowed, body: page},
		fixtureProxy + "/doUpdate":        {status: http.StatusNoContent},
		fixtureProxy + "/getChoicesForUI": {status: http.StatusOK, body: choices},
	}}
}

func fixtureJob(s *fakeStapler) *Job {
	jenkins := &Jenkins{Requester: &MockRequester{DoFunc: s.do}}
	return &Job{Jenkins: jenkins, Base: fixtureJobBase}
}

func TestGetBuildFormParsesCapturedFixture(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), "")

	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	require.Len(t, form.Parameters, 3)
	env, server, dryRun := form.Parameters[0], form.Parameters[1], form.Parameters[2]

	assert.Equal(t, "ENV", env.Name)
	assert.Equal(t, "ChoiceParameter", env.Type)
	assert.Equal(t, []Choice{
		{Value: "dev", Label: "dev"},
		{Value: "staging", Label: "staging"},
		{Value: "production", Label: "production"},
	}, env.Choices)
	assert.Empty(t, env.ReferencedParameters)

	assert.Equal(t, "SERVER", server.Name)
	assert.Equal(t, "CascadeChoiceParameter", server.Type)
	assert.Empty(t, server.Choices, "the server-rendered UNAVAILABLE placeholder must not be trusted")
	assert.Equal(t, []string{"ENV"}, server.ReferencedParameters)

	assert.Equal(t, "DRY_RUN", dryRun.Name)
	assert.Empty(t, dryRun.Choices)

	assert.Equal(t, []string{"SERVER"}, form.Dependents("ENV"))
}

// Jenkins answers GET <job>/build with 405 but a fully rendered body.
func TestGetBuildFormRecoversPageFrom405(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), "")
	require.Equal(t, http.StatusMethodNotAllowed, stapler.routes[fixtureJobBase+"/build"].status)

	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)
	assert.Len(t, form.Parameters, 3)
}

func TestGetBuildFormStatusHandling(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "405 is how Jenkins renders the form", status: http.StatusMethodNotAllowed},
		{name: "200", status: http.StatusOK},
		{name: "other 2xx", status: http.StatusNonAuthoritativeInfo},
		{name: "404", status: http.StatusNotFound, wantErr: true},
		{name: "403", status: http.StatusForbidden, wantErr: true},
		{name: "500", status: http.StatusInternalServerError, wantErr: true},
		{name: "503", status: http.StatusServiceUnavailable, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stapler := newFakeStapler(loadBuildFormFixture(t), "")
			stapler.routes[fixtureJobBase+"/build"] = staplerReply{status: tt.status, body: loadBuildFormFixture(t)}

			form, err := fixtureJob(stapler).GetBuildForm(context.Background())
			if tt.wantErr {
				assert.ErrorContains(t, err, fmt.Sprintf("HTTP %d", tt.status))
				return
			}
			require.NoError(t, err)
			assert.Len(t, form.Parameters, 3)
		})
	}
}

// Without the crumb headers the proxy answers a bare 403, so a crumb-less
// render is rejected before anything is sent.
func TestResolveWithoutCrumbFailsBeforeSending(t *testing.T) {
	page := strings.Replace(loadBuildFormFixture(t), `data-crumb-value="`+fixtureCrumb+`"`, "", 1)
	stapler := newFakeStapler(page, `[["a"],["a"]]`)
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)
	rendered := len(stapler.calls)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	assert.ErrorContains(t, err, "crumb")
	assert.Len(t, stapler.calls, rendered, "no request may reach Jenkins without a crumb")
}

func TestResolveSendsStaplerHeaderContract(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), `[["prod-app-1"],["prod-app-1"]]`)
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	require.NoError(t, err)

	updates := stapler.callsTo(fixtureProxy + "/doUpdate")
	require.Len(t, updates, 1)
	headers := updates[0].headers

	assert.Equal(t, staplerContentType, headers.Get("Content-Type"), "Content-Type: without it Stapler does not route the method (404)")
	assert.Equal(t, fixtureCrumb, headers.Get("Crumb"), "Crumb: without it Jenkins rejects the call (403)")
	assert.Equal(t, fixtureCrumb, headers.Get("Jenkins-Crumb"), "Jenkins-Crumb: without it Jenkins rejects the call (403)")
	assert.Equal(t, fixtureCookie, headers.Get("Cookie"), "Cookie: without the render's session the bound uuid is stale (404)")
	assert.ElementsMatch(t, []string{"Content-Type", "Crumb", "Jenkins-Crumb", "Cookie"}, headerNames(headers), "doUpdate must carry exactly the four stapler headers")
}

func TestResolveJoinsReferencedParametersWithLESEP(t *testing.T) {
	tests := []struct {
		name       string
		referenced string
		values     map[string]string
		want       string
	}{
		{
			name:       "one referenced parameter",
			referenced: "ENV",
			values:     map[string]string{"ENV": "production"},
			want:       "ENV=production",
		},
		{
			name:       "two referenced parameters",
			referenced: "ENV,DRY_RUN",
			values:     map[string]string{"ENV": "production", "DRY_RUN": "false"},
			want:       "ENV=production__LESEP__DRY_RUN=false",
		},
		{
			name:       "three referenced parameters",
			referenced: "ENV,DRY_RUN,REGION",
			values:     map[string]string{"ENV": "staging", "DRY_RUN": "true", "REGION": "eu"},
			want:       "ENV=staging__LESEP__DRY_RUN=true__LESEP__REGION=eu",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := strings.Replace(loadBuildFormFixture(t), `data-referenced-parameters="ENV"`, `data-referenced-parameters="`+tt.referenced+`"`, 1)
			stapler := newFakeStapler(page, `[["a"],["a"]]`)
			form, err := fixtureJob(stapler).GetBuildForm(context.Background())
			require.NoError(t, err)

			_, err = form.Resolve(context.Background(), "SERVER", tt.values)
			require.NoError(t, err)

			updates := stapler.callsTo(fixtureProxy + "/doUpdate")
			require.Len(t, updates, 1)
			var args []string
			require.NoError(t, json.Unmarshal([]byte(updates[0].body), &args))
			require.Len(t, args, 1, "doUpdate takes one argument")
			assert.Equal(t, tt.want, args[0])
			assert.NotContains(t, args[0], ",", "a comma separator is swallowed into the value and yields wrong choices")
		})
	}
}

func TestResolvePairsLabelsAndValues(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t),
		`[["EU Production:selected","US Production:disabled","Staging:disabled:selected"],["prod-eu","prod-us","staging"]]`)
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	choices, err := form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	require.NoError(t, err)
	assert.Equal(t, []Choice{
		{Value: "prod-eu", Label: "EU Production", Selected: true},
		{Value: "prod-us", Label: "US Production", Disabled: true},
		{Value: "staging", Label: "Staging", Selected: true, Disabled: true},
	}, choices)
}

func TestResolveWritesChoicesBackToForm(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), `[["App 1","App 2"],["prod-app-1","prod-app-2"]]`)
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)
	require.Empty(t, form.Parameters[1].Choices)

	choices, err := form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	require.NoError(t, err)

	require.Equal(t, "SERVER", form.Parameters[1].Name)
	assert.Equal(t, choices, form.Parameters[1].Choices)
	assert.Equal(t, []Choice{{Value: "prod-app-1", Label: "App 1"}, {Value: "prod-app-2", Label: "App 2"}}, form.Parameters[1].Choices)
}

// DynamicReferenceParameter returns markup, which []Choice cannot express.
func TestResolveRejectsDynamicReferenceParameter(t *testing.T) {
	page := strings.Replace(loadBuildFormFixture(t), "cascade-choice-parameter-data-holder", "dynamic-reference-parameter-data-holder", 1)
	page = strings.Replace(page, "methods=doUpdate,getChoicesForUI", "methods=doUpdate,getChoicesAsStringForUI", 1)
	stapler := newFakeStapler(page, "")
	stapler.routes[fixtureProxy+"/getChoicesAsStringForUI"] = staplerReply{status: http.StatusOK, body: `"<b>markup</b>"`}
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)
	require.Equal(t, "DynamicReferenceParameter", form.Parameters[1].Type)
	rendered := len(stapler.calls)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	assert.ErrorContains(t, err, "unsupported parameter type")
	assert.Len(t, stapler.calls, rendered, "an unsupported parameter must not be driven")
}

func TestPairChoicesStripsMarkersFromLabelsOnly(t *testing.T) {
	tests := []struct {
		name  string
		label string
		value string
		want  Choice
	}{
		{name: "selected", label: "EU:selected", value: "eu", want: Choice{Value: "eu", Label: "EU", Selected: true}},
		{name: "disabled", label: "EU:disabled", value: "eu", want: Choice{Value: "eu", Label: "EU", Disabled: true}},
		{name: "selected then disabled", label: "EU:selected:disabled", value: "eu", want: Choice{Value: "eu", Label: "EU", Selected: true, Disabled: true}},
		{name: "disabled then selected", label: "EU:disabled:selected", value: "eu", want: Choice{Value: "eu", Label: "EU", Selected: true, Disabled: true}},
		{name: "label and value differ", label: "EU Production", value: "prod-eu", want: Choice{Value: "prod-eu", Label: "EU Production"}},
		{name: "value ending in a marker is verbatim", label: "Odd", value: "odd:selected", want: Choice{Value: "odd:selected", Label: "Odd"}},
		{name: "value ending in both markers is verbatim", label: "Odd:selected", value: "odd:disabled:selected", want: Choice{Value: "odd:disabled:selected", Label: "Odd", Selected: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, []Choice{tt.want}, pairChoices([]string{tt.label}, []string{tt.value}))
		})
	}
}

// An unapproved or non-sandboxed script throws server-side, and Jenkins still
// answers 200 with [[],[]].
func TestResolveReportsEmptyChoicesAsError(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), `[[],[]]`)
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	assert.ErrorIs(t, err, ErrNoChoices)
}

// A parameter whose stored XML lacks its <parameters> map NPEs in doUpdate.
func TestResolveReportsDoUpdateServerError(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), `[["a"],["a"]]`)
	stapler.routes[fixtureProxy+"/doUpdate"] = staplerReply{status: http.StatusInternalServerError}
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	assert.NotErrorIs(t, err, ErrNoChoices)
	assert.Empty(t, stapler.callsTo(fixtureProxy+"/getChoicesForUI"), "choices must not be fetched after a failed doUpdate")
}

// A bound uuid belongs to one render; once it is gone the proxy 404s and the
// caller must re-render the form rather than have it swapped underneath it.
func TestResolveReportsStaleProxyWithoutRerendering(t *testing.T) {
	stapler := newFakeStapler(loadBuildFormFixture(t), "")
	stapler.routes[fixtureProxy+"/doUpdate"] = staplerReply{status: http.StatusNotFound}
	form, err := fixtureJob(stapler).GetBuildForm(context.Background())
	require.NoError(t, err)

	_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
	assert.ErrorIs(t, err, ErrStaleBuildForm)
	assert.Len(t, stapler.callsTo(fixtureJobBase+"/build"), 1, "Resolve must not re-render the form")
}

// Jenkins answers errors with an HTML page. Requester.Do decodes JSON before
// the status can be seen, so the status must be checked on the raw body first.
func TestResolveReportsStatusDespiteHTMLErrorBody(t *testing.T) {
	const errorPage = "<html><body>Not Found</body></html>"
	tests := []struct {
		name   string
		status int
		check  func(t *testing.T, err error)
	}{
		{name: "404 is a stale form", status: http.StatusNotFound, check: func(t *testing.T, err error) {
			assert.ErrorIs(t, err, ErrStaleBuildForm)
		}},
		{name: "500 names the status", status: http.StatusInternalServerError, check: func(t *testing.T, err error) {
			assert.ErrorContains(t, err, "HTTP 500")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stapler := newFakeStapler(loadBuildFormFixture(t), "")
			stapler.routes[fixtureProxy+"/getChoicesForUI"] = staplerReply{status: tt.status, body: errorPage}
			form, err := fixtureJob(stapler).GetBuildForm(context.Background())
			require.NoError(t, err)

			_, err = form.Resolve(context.Background(), "SERVER", map[string]string{"ENV": "production"})
			tt.check(t, err)
		})
	}
}

func TestParseBuildFormKeepsUnsupportedActiveChoicesType(t *testing.T) {
	page := strings.Replace(loadBuildFormFixture(t), "cascade-choice-parameter-data-holder", "dynamic-reference-parameter-data-holder", 1)

	form, err := parseBuildForm(page)
	require.NoError(t, err)
	require.Len(t, form.Parameters, 3)
	assert.Equal(t, "SERVER", form.Parameters[1].Name)
	assert.Equal(t, "DynamicReferenceParameter", form.Parameters[1].Type)
	assert.Empty(t, form.Parameters[1].Choices)
	assert.Equal(t, []string{"ENV"}, form.Parameters[1].ReferencedParameters)
}

// referenceParameterPage renders one DynamicReferenceParameter the way
// uno-choice's DynamicReferenceParameter/index.jelly does, with body as the
// markup inside its parameter div.
func referenceParameterPage(holderClass, body string) string {
	return `<html><head></head><body><form><div name="parameter" id="choice-parameter-1">` +
		`<input type="hidden" name="name" value="SUMMARY" />` + body + `</div>` +
		`<span class="` + holderClass + `" data-proxy-name="proxy_id1" data-referenced-parameters="ENV" ` +
		`data-param-name="choice-parameter-1" data-name="SUMMARY"></span></form></body></html>`
}

func TestParseBuildFormDetectsOmitValueField(t *testing.T) {
	const reference = "dynamic-reference-parameter-data-holder"
	tests := []struct {
		name   string
		holder string
		body   string
		omit   bool
	}{
		{"formatted HTML with the hidden value input", reference,
			`<div id="formattedHtml"><b>plan</b></div><input type="text" name="value" value="" class="jenkins-hidden" />`, false},
		{"formatted HTML with omitValueField", reference,
			`<div id="formattedHtml"><b>plan</b></div>`, true},
		{"text box with omitValueField", reference,
			`<input id="inputElement_x" type="text" value="note" readonly="readonly" disabled="disabled" class="jenkins-input"/>`, true},
		{"script renders its own value input", reference,
			`<div id="formattedHtml"><input type="text" name="value" value="custom" /></div>`, false},
		{"cascade choice is never omitted", "cascade-choice-parameter-data-holder",
			`<select name="value"><option value="a">a</option></select>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form, err := parseBuildForm(referenceParameterPage(tt.holder, tt.body))
			require.NoError(t, err)
			require.Len(t, form.Parameters, 1)
			assert.Equal(t, tt.omit, form.Parameters[0].OmitValueField)
		})
	}
}

// activeChoicePage renders one plain ChoiceParameter the way uno-choice's
// common/choiceParameterCommon.jelly does, with body as the markup inside its
// parameter div, followed by a core boolean parameter.
func activeChoicePage(body string) string {
	return `<html><head></head><body><form>` +
		`<div name="parameter" id="choice-parameter-1" class="active-choice">` +
		`<input type="hidden" name="name" value="TARGETS" />` + body + `</div>` +
		`<div name="parameter"><input name="name" type="hidden" value="DRY_RUN">` +
		`<input name="value" checked="true" type="checkbox"></div>` +
		`</form></body></html>`
}

func TestParseBuildFormReadsChoiceTypeAndFilter(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		multiple   bool
		filterable bool
		choices    []Choice
	}{
		{"single select", `<select name="value"><option value="a">A</option><option value="b" selected="selected">B</option></select>`,
			false, false, []Choice{{Value: "a", Label: "A"}, {Value: "b", Label: "B", Selected: true}}},
		{"multi select", `<select name="value" multiple="multiple" size="1"><option value="a">A</option></select>`,
			true, false, []Choice{{Value: "a", Label: "A"}}},
		{"checkbox", `<div class="dynamic_checkbox"><div class="jenkins-checkbox">` +
			`<input json="a" name="value" value="a" type="checkbox" title="Alpha" alt="Alpha" checked="true" /><label>Alpha</label>` +
			`</div><div class="jenkins-checkbox">` +
			`<input disabled="true" json="b" name="value" value="b" type="checkbox" title="Beta" alt="Beta" /><label>Beta</label></div></div>`,
			true, false, []Choice{{Value: "a", Label: "Alpha", Selected: true}, {Value: "b", Label: "Beta", Disabled: true}}},
		{"radio", `<div class="ac-container"><div class="jenkins-radio">` +
			`<input json="a" alt="Alpha" otherid="r1" name="TARGETS" value="a" type="radio" class="jenkins-radio__input" /><label>Alpha</label>` +
			`<input json="a" name="value" value="a" type="hidden" id="r1" title="Alpha" /></div>` +
			`<div class="jenkins-radio"><input json="b" alt="Beta" otherid="r2" checked="checked" name="TARGETS" value="b" type="radio" class="jenkins-radio__input" /><label>Beta</label>` +
			`<input json="b" name="value" value="b" type="hidden" id="r2" title="Beta" /></div></div>`,
			false, false, []Choice{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta", Selected: true}}},
		{"filterable", `<select name="value"><option value="a">A</option></select>` +
			`<input class='uno_choice_filter jenkins-input' type='text' value='' name='test' placeholder='Filter'/>`,
			false, true, []Choice{{Value: "a", Label: "A"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form, err := parseBuildForm(activeChoicePage(tt.body))
			require.NoError(t, err)
			require.Len(t, form.Parameters, 2)
			p := form.Parameters[0]
			assert.Equal(t, "ChoiceParameter", p.Type)
			assert.Equal(t, tt.multiple, p.Multiple, "Multiple")
			assert.Equal(t, tt.filterable, p.Filterable, "Filterable")
			assert.Equal(t, tt.choices, p.Choices)

			boolean := form.Parameters[1]
			assert.False(t, boolean.Multiple, "a boolean's checkbox is not a multi-value choice")
			assert.Empty(t, boolean.Choices)
		})
	}
}

func TestParseBuildFormKeepsCascadeChoiceTypeBeforeResolve(t *testing.T) {
	page := referenceParameterPage("cascade-choice-parameter-data-holder",
		`<select name="value" multiple="multiple"><option value="UNAVAILABLE">UNAVAILABLE</option></select>`+
			`<input class='uno_choice_filter jenkins-input' type='text' name='test'/>`)
	page = strings.Replace(page, `<div name="parameter" id="choice-parameter-1">`, `<div name="parameter" id="choice-parameter-1" class="active-choice">`, 1)

	form, err := parseBuildForm(page)
	require.NoError(t, err)
	p := form.Parameters[0]
	assert.Equal(t, "CascadeChoiceParameter", p.Type)
	assert.True(t, p.Multiple)
	assert.True(t, p.Filterable)
	assert.Nil(t, p.Choices, "server-rendered fallback options are dropped until Resolve")
}

func TestParseBuildFormRejectsMissingParameters(t *testing.T) {
	_, err := parseBuildForm("<html><body>not a build form</body></html>")
	assert.ErrorIs(t, err, ErrUnsupportedForm)
}

func headerNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	return names
}
