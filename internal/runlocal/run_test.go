package runlocal

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRunner_happyPath(t *testing.T) {
	var out bytes.Buffer
	runner := DefaultRunner{}
	results, err := runner.Run(t.Context(), []Check{
		{Name: "greet", Cmd: "echo hello"},
	}, &out)

	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, "greet", results[0].Name)
	assert.Equal(t, 0, results[0].ExitCode)
	assert.Contains(t, results[0].Output, "hello")
	assert.Contains(t, out.String(), "hello")
	assert.Contains(t, out.String(), "▶ greet: echo hello")
}

func TestDefaultRunner_nonZeroExit(t *testing.T) {
	var out bytes.Buffer
	runner := DefaultRunner{}
	results, err := runner.Run(t.Context(), []Check{
		{Name: "fail", Cmd: "false"},
	}, &out)

	// Non-zero exit is not a Run-level error.
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, "fail", results[0].Name)
	assert.NotEqual(t, 0, results[0].ExitCode)
	assert.Nil(t, results[0].Err)
}

func TestDefaultRunner_failFastStops(t *testing.T) {
	var out bytes.Buffer
	runner := DefaultRunner{}
	results, err := runner.Run(t.Context(), []Check{
		{Name: "fail", Cmd: "false", FailFast: true},
		{Name: "skip", Cmd: "echo should-not-run"},
	}, &out)

	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.NotContains(t, out.String(), "should-not-run")
}

func TestDefaultRunner_timeout(t *testing.T) {
	var out bytes.Buffer
	runner := DefaultRunner{}
	results, err := runner.Run(t.Context(), []Check{
		{
			Name:    "slow",
			Cmd:     "sleep 5",
			Timeout: 100 * time.Millisecond,
		},
	}, &out)

	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.NotEqual(t, 0, results[0].ExitCode)
	// The check sleeps 5s with a 100ms timeout, so it must be killed
	// well before completing. Allow generous slack for process-kill
	// latency on loaded CI runners while still proving the timeout fired.
	assert.Less(t, results[0].Duration, 4*time.Second,
		"expected the check to be killed before its 5s sleep completed, got %v",
		results[0].Duration)
	assert.True(
		t,
		strings.Contains(out.String(), "▶ slow: sleep 5"),
		"expected header in output",
	)
}
