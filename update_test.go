package main

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.abhg.dev/testing/stub"
)

func TestCurrentCommit(t *testing.T) {
	t.Run("StampedCommit", func(t *testing.T) {
		defer stub.Value(&_commit, "stampedsha")()

		assert.Equal(t, "stampedsha", currentCommit())
	})

	t.Run("FallbackToVCS", func(t *testing.T) {
		defer stub.Value(&_commit, "")()
		defer stub.Func(&_debugReadBuildInfo, &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "vcssha"},
			},
		}, true)()

		assert.Equal(t, "vcssha", currentCommit())
	})

	t.Run("Unavailable", func(t *testing.T) {
		defer stub.Value(&_commit, "")()
		defer stub.Func(&_debugReadBuildInfo, (*debug.BuildInfo)(nil), false)()

		assert.Empty(t, currentCommit())
	})
}

func TestSkipUpdateCheck(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "Empty", command: "", want: true},
		{name: "Version", command: "version", want: true},
		{name: "Update", command: "update", want: true},
		{name: "Internal", command: "internal submit", want: true},
		{name: "ShellCompletion", command: "shell completion bash", want: true},
		{name: "DumpMarkdown", command: "dumpmd", want: true},
		{name: "BranchCreate", command: "branch create <name>", want: false},
		{name: "RepoSync", command: "repo sync", want: false},
		{name: "LogShort", command: "log short", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, skipUpdateCheck(tt.command))
		})
	}
}
