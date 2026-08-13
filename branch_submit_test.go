package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog"
)

// stubStaleChecker is a hand-written fake for gitBaseStaleChecker.
// The interface is tiny (two methods), so a fake is clearer here than
// a generated mock.
type stubStaleChecker struct {
	upstream string
	upErr    error
	behind   int
	abErr    error
}

func (s stubStaleChecker) BranchUpstream(context.Context, string) (string, error) {
	return s.upstream, s.upErr
}

func (s stubStaleChecker) CommitAheadBehind(context.Context, string, string) (int, int, error) {
	return 0, s.behind, s.abErr
}

func TestWarnIfBaseStale(t *testing.T) {
	t.Run("Stale", func(t *testing.T) {
		var buf bytes.Buffer
		got := warnIfBaseStale(t.Context(), silog.New(&buf, nil), stubStaleChecker{
			upstream: "origin/main",
			behind:   5,
		}, "main")
		assert.True(t, got, "stale base must be reported")
		assert.Contains(t, buf.String(), "behind")
		assert.Contains(t, buf.String(), "origin/main")
	})

	t.Run("UpToDate", func(t *testing.T) {
		var buf bytes.Buffer
		got := warnIfBaseStale(t.Context(), silog.New(&buf, nil), stubStaleChecker{
			upstream: "origin/main",
			behind:   0,
		}, "main")
		assert.False(t, got)
		assert.Empty(t, buf.String(), "no warning when up to date")
	})

	t.Run("NoUpstream", func(t *testing.T) {
		var buf bytes.Buffer
		got := warnIfBaseStale(t.Context(), silog.New(&buf, nil), stubStaleChecker{
			upErr: git.ErrNotExist,
		}, "feature")
		assert.False(t, got, "no upstream means nothing to compare against")
		assert.Empty(t, buf.String())
	})

	t.Run("AheadBehindError", func(t *testing.T) {
		var buf bytes.Buffer
		got := warnIfBaseStale(t.Context(), silog.New(&buf, nil), stubStaleChecker{
			upstream: "origin/main",
			abErr:    assert.AnError,
		}, "main")
		assert.False(t, got, "errors must not block the summary")
		assert.Empty(t, buf.String())
	})
}
