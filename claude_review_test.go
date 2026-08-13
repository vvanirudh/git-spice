package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/silog"
)

func TestClaudeReviewCmd_graphContext_disabled(t *testing.T) {
	// With graph integration disabled (the default), no external tool
	// is invoked and the context is empty, so reviews degrade to the
	// diff-only behavior.
	got := (&claudeReviewCmd{}).graphContext(
		t.Context(), silog.Nop(), &claude.Config{}, "/repo", "main")
	assert.Empty(t, got)
}
