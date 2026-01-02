package gojenkins

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- PipelineRun.SaveTextLog ---

func newMockPipelineRun(jenkins *Jenkins) *PipelineRun {
	job := &Job{
		Jenkins: jenkins,
		Raw:     &JobResponse{Name: "my-pipeline"},
		Base:    "/job/my-pipeline",
	}
	return &PipelineRun{
		Job:  job,
		Base: "/job/my-pipeline/1",
		ID:   "1",
	}
}

func TestPipelineRun_SaveTextLog_Success(t *testing.T) {
	jenkins := newMockJenkins()
	tmpFile, err := os.CreateTemp(t.TempDir(), "jenkins-log-*")
	assert.NoError(t, err)
	defer tmpFile.Close()

	jenkins.Requester.(*MockRequester).SaveToFileFunc = func(ctx context.Context, endpoint string, filepath string, querystring map[string]string) (*os.File, *http.Response, error) {
		assert.Equal(t, "/job/my-pipeline/1/consoleText", endpoint)
		return tmpFile, &http.Response{StatusCode: 200}, nil
	}

	run := newMockPipelineRun(jenkins)
	file, err := run.SaveTextLog(context.Background(), tmpFile.Name())
	assert.NoError(t, err)
	assert.Equal(t, tmpFile, file)
}

func TestPipelineRun_SaveTextLog_Error(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).err = assert.AnError

	run := newMockPipelineRun(jenkins)
	file, err := run.SaveTextLog(context.Background(), "/tmp/log.txt")
	assert.Error(t, err)
	assert.Nil(t, file)
}

func TestPipelineRun_SaveTextLog_UsesRunBase(t *testing.T) {
	jenkins := newMockJenkins()
	var capturedEndpoint string
	jenkins.Requester.(*MockRequester).SaveToFileFunc = func(ctx context.Context, endpoint string, filepath string, querystring map[string]string) (*os.File, *http.Response, error) {
		capturedEndpoint = endpoint
		return nil, &http.Response{StatusCode: 200}, nil
	}

	job := &Job{Jenkins: jenkins, Raw: &JobResponse{Name: "pipe"}, Base: "/job/pipe"}
	run := &PipelineRun{Job: job, Base: "/job/pipe/42", ID: "42"}

	_, _ = run.SaveTextLog(context.Background(), "/tmp/out.txt")
	assert.Equal(t, "/job/pipe/42/consoleText", capturedEndpoint)
}

// --- PipelineRun.Replay ---

func TestPipelineRun_Replay_Success200(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).PostFunc = func(ctx context.Context, endpoint string, payload io.Reader, response interface{}, query map[string]string) (*http.Response, error) {
		assert.Equal(t, "/job/my-pipeline/1/replay/run", endpoint)
		return &http.Response{StatusCode: 200}, nil
	}

	run := newMockPipelineRun(jenkins)
	ok, err := run.Replay(context.Background(), map[string]string{"branch": "main"})
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestPipelineRun_Replay_Success201(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).PostFunc = func(ctx context.Context, endpoint string, payload io.Reader, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{StatusCode: 201}, nil
	}

	run := newMockPipelineRun(jenkins)
	ok, err := run.Replay(context.Background(), nil)
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestPipelineRun_Replay_RequesterError(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).err = assert.AnError

	run := newMockPipelineRun(jenkins)
	ok, err := run.Replay(context.Background(), nil)
	assert.Error(t, err)
	assert.False(t, ok)
}

func TestPipelineRun_Replay_BadStatus(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).PostFunc = func(ctx context.Context, endpoint string, payload io.Reader, response interface{}, query map[string]string) (*http.Response, error) {
		return &http.Response{StatusCode: 403}, nil
	}

	run := newMockPipelineRun(jenkins)
	ok, err := run.Replay(context.Background(), nil)
	assert.Error(t, err)
	assert.Equal(t, "403", err.Error())
	assert.False(t, ok)
}

// --- PipelineRun.GetReplayScript ---

func TestPipelineRun_GetReplayScript_Success(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		if s, ok := response.(*string); ok {
			*s = `<html><body><textarea>pipeline { agent any; stages { stage('Build') { steps { echo 'hello' } } } }</textarea></body></html>`
		}
		return &http.Response{StatusCode: 200}, nil
	}

	run := newMockPipelineRun(jenkins)
	script, err := run.GetReplayScript(context.Background())
	assert.NoError(t, err)
	assert.Contains(t, script, "pipeline {")
}

func TestPipelineRun_GetReplayScript_UsesCorrectEndpoint(t *testing.T) {
	jenkins := newMockJenkins()
	var capturedEndpoint string
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		capturedEndpoint = endpoint
		return &http.Response{StatusCode: 200}, nil
	}

	run := newMockPipelineRun(jenkins)
	_, _ = run.GetReplayScript(context.Background())
	assert.Equal(t, "/job/my-pipeline/1/replay/", capturedEndpoint)
}

func TestPipelineRun_GetReplayScript_NoTextarea(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).GetFunc = func(ctx context.Context, endpoint string, response interface{}, query map[string]string) (*http.Response, error) {
		if s, ok := response.(*string); ok {
			*s = `<html><body><p>No textarea here</p></body></html>`
		}
		return &http.Response{StatusCode: 200}, nil
	}

	run := newMockPipelineRun(jenkins)
	script, err := run.GetReplayScript(context.Background())
	assert.NoError(t, err)
	assert.NotEmpty(t, script)
}

func TestPipelineRun_GetReplayScript_RequesterError(t *testing.T) {
	jenkins := newMockJenkins()
	jenkins.Requester.(*MockRequester).err = assert.AnError

	run := newMockPipelineRun(jenkins)
	script, err := run.GetReplayScript(context.Background())
	assert.Error(t, err)
	assert.Empty(t, script)
}
