package main

import (
	"context"

	"github.com/alecthomas/kong"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/text"
)

// showCmd is an alias for 'gs ls -aS':
// it lists all tracked branches
// and includes change request status information.
type showCmd struct {
	logShortCmd
}

func (*showCmd) AfterApply(kctx *kong.Context) error {
	return bindListHandler(kctx)
}

func (*showCmd) Help() string {
	return text.Dedent(`
		Lists all tracked branches, not just the current stack,
		and includes change request status information for each branch.

		This is equivalent to 'gs ls -aS'.

		With --json, prints output to stdout as a stream of JSON objects.
		See https://abhinav.github.io/git-spice/cli/json/ for details.
	`)
}

func (cmd *showCmd) Run(
	ctx context.Context,
	kctx *kong.Context,
	wt *git.Worktree,
	listHandler ListHandler,
) (err error) {
	cmd.All = true
	cmd.CRStatus = true
	return cmd.run(ctx, kctx, nil, wt, listHandler)
}
