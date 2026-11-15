package gojenkins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ResponseDecodeError describes a response that could not be decoded as JSON.
// Its message excludes URLs, headers and response bodies. HTMLResponseBody
// provides explicitly requested diagnostics; Unwrap preserves the decoding error.
type ResponseDecodeError struct {
	StatusCode  int
	ContentType string
	Err         error

	body          string
	bodyTruncated bool
}

func (e *ResponseDecodeError) Error() string {
	detail := "invalid JSON"
	if e.ContentType == "text/html" || e.ContentType == "application/xhtml+xml" {
		detail = "HTML instead of JSON; check authentication and Jenkins/reverse-proxy logs"
	}
	return fmt.Sprintf("Jenkins returned HTTP %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), detail)
}

func (e *ResponseDecodeError) Unwrap() error { return e.Err }

// HTMLResponseBody returns the original HTML response, capped at 256 KiB, and
// whether it is incomplete. It may contain sensitive data: never log or copy it
// automatically. An empty body means HTML diagnostics are unavailable.
func (e *ResponseDecodeError) HTMLResponseBody() (body string, truncated bool) {
	return e.body, e.bodyTruncated
}

const maxErrorBodyBytes = 256 * 1024

func (e *ResponseDecodeError) captureHTML(decoder *json.Decoder, remaining io.Reader) {
	var syntax *json.SyntaxError
	if !errors.As(e.Err, &syntax) {
		return
	}
	// A failed syntax scan leaves its bytes in Buffered. Include those bytes
	// before reading the remainder, without buffering successful JSON responses.
	data, err := io.ReadAll(io.LimitReader(io.MultiReader(decoder.Buffered(), remaining), maxErrorBodyBytes+1))
	isHTML := e.ContentType == "text/html" || e.ContentType == "application/xhtml+xml"
	if !isHTML && http.DetectContentType(bytes.TrimSpace(data)) != "text/html; charset=utf-8" {
		return
	}
	e.bodyTruncated = len(data) > maxErrorBodyBytes || err != nil
	if len(data) > maxErrorBodyBytes {
		data = data[:maxErrorBodyBytes]
	}
	e.body = string(data)
}
