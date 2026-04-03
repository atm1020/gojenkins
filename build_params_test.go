package gojenkins

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseBuildParameters_StringParam(t *testing.T) {
	html := `<div id="main-panel">
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">APP_NAME</div>
  <div class="jenkins-form-description">Application name</div>
  <div class="setting-main">
    <div class="jenkins-quote jenkins-quote--full-width jenkins-quote--monospace" id="example">
      my-service
      <span><button tooltip="Copy" text="my-service" type="button" class="copy-button jenkins-button jenkins-button--tertiary jenkins-copy-button"></button></span>
    </div>
  </div>
  <div class="validation-error-area"></div>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 1)
	assert.Equal(t, "APP_NAME", params[0].Name)
	assert.Equal(t, "my-service", params[0].Value)
}

func TestParseBuildParameters_TextParam(t *testing.T) {
	html := `<div id="main-panel">
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">CHANGELOG</div>
  <div class="jenkins-form-description">Multiline changelog</div>
  <div class="setting-main">
    <pre class="jenkins-readonly">- Fixed login bug
- Updated dependencies
- Improved performance</pre>
  </div>
  <div class="validation-error-area"></div>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 1)
	assert.Equal(t, "CHANGELOG", params[0].Name)
	assert.Contains(t, params[0].Value, "Fixed login bug")
}

func TestParseBuildParameters_BooleanParam(t *testing.T) {
	html := `<div id="main-panel">
<div>
  <span class="jenkins-checkbox">
    <input name="value" checked="true" disabled="true" type="checkbox" class=" ">
    <label class="attach-previous ">NOTIFY_SLACK</label>
  </span>
  <div class="jenkins-checkbox__description">Send Slack notification</div>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 1)
	assert.Equal(t, "NOTIFY_SLACK", params[0].Name)
	assert.Equal(t, "true", params[0].Value)
}

func TestParseBuildParameters_UncheckedBoolean(t *testing.T) {
	html := `<div id="main-panel">
<div>
  <span class="jenkins-checkbox">
    <input name="value" disabled="true" type="checkbox" class=" ">
    <label class="attach-previous ">DEBUG_MODE</label>
  </span>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 1)
	assert.Equal(t, "DEBUG_MODE", params[0].Name)
	assert.Equal(t, "false", params[0].Value)
}

func TestParseBuildParameters_AllTypes(t *testing.T) {
	// Mirrors real Jenkins HTML with string, text, boolean, and choice params
	html := `<div id="main-panel">
<div class="jenkins-app-bar"><h1>Parameters</h1></div>
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">APP_NAME</div>
  <div class="jenkins-form-description">Application name</div>
  <div class="setting-main">
    <div class="jenkins-quote jenkins-quote--full-width jenkins-quote--monospace" id="example">
      my-service
      <span><button tooltip="Copy" text="my-service" type="button" class="copy-button jenkins-button jenkins-button--tertiary jenkins-copy-button"></button></span>
    </div>
  </div>
  <div class="validation-error-area"></div>
</div>
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">CHANGELOG</div>
  <div class="jenkins-form-description">Multiline changelog</div>
  <div class="setting-main">
    <pre class="jenkins-readonly">- Fixed login bug
- Updated dependencies</pre>
  </div>
  <div class="validation-error-area"></div>
</div>
<div>
  <span class="jenkins-checkbox">
    <input name="value" checked="true" disabled="true" type="checkbox" class=" ">
    <label class="attach-previous ">NOTIFY_SLACK</label>
  </span>
  <div class="jenkins-checkbox__description">Send Slack notification</div>
</div>
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">REGION</div>
  <div class="jenkins-form-description">AWS region</div>
  <div class="setting-main">
    <div class="jenkins-quote jenkins-quote--full-width jenkins-quote--monospace" id="example">
      EU
      <span><button tooltip="Copy" text="EU" type="button" class="copy-button jenkins-button"></button></span>
    </div>
  </div>
  <div class="validation-error-area"></div>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 4)

	assert.Equal(t, "APP_NAME", params[0].Name)
	assert.Equal(t, "my-service", params[0].Value)

	assert.Equal(t, "CHANGELOG", params[1].Name)
	assert.Contains(t, params[1].Value, "Fixed login bug")

	assert.Equal(t, "NOTIFY_SLACK", params[2].Name)
	assert.Equal(t, "true", params[2].Value)

	assert.Equal(t, "REGION", params[3].Name)
	assert.Equal(t, "EU", params[3].Value)
}

func TestParseBuildParameters_NoParams(t *testing.T) {
	html := `<div id="main-panel"><h1>No parameters</h1></div>`
	params := parseBuildParameters(html)
	assert.Empty(t, params)
}

func TestParseBuildParameters_EmptyValue(t *testing.T) {
	html := `<div id="main-panel">
<div class="jenkins-form-item tr ">
  <div class="jenkins-form-label help-sibling">OPTIONAL_PARAM</div>
  <div class="setting-main">
    <div class="jenkins-quote jenkins-quote--full-width jenkins-quote--monospace">
      <span><button text="" type="button" class="copy-button jenkins-button"></button></span>
    </div>
  </div>
  <div class="validation-error-area"></div>
</div>
</div>`

	params := parseBuildParameters(html)
	assert.Len(t, params, 1)
	assert.Equal(t, "OPTIONAL_PARAM", params[0].Name)
	assert.Equal(t, "", params[0].Value)
}
