package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"go.abhg.dev/gs/internal/cli/update"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/ui"
)

// _newUpdateChecker builds a Checker for the running binary.
// It is a variable so tests can stub the update check.
var _newUpdateChecker = func(log *silog.Logger) *update.Checker {
	return &update.Checker{
		Owner:         update.DefaultOwner,
		Repo:          update.DefaultRepo,
		Branch:        update.DefaultBranch,
		CurrentCommit: currentCommit(),
		StateDir:      _configDir,
		Token:         os.Getenv("GITHUB_TOKEN"),
		Log:           log,
	}
}

// notifyUpdateAvailable prints a one-time notice
// when the installed commit is behind the latest commit on main.
//
// It is best-effort: any failure is logged at debug level and ignored
// so that it never blocks or fails the requested command.
func (cmd *mainCmd) notifyUpdateAvailable(
	ctx context.Context,
	kctx *kong.Context,
	log *silog.Logger,
	view ui.View,
) {
	if !cmd.Globals.UpdateCheck {
		return
	}

	// Only notify in an interactive terminal;
	// scripts and pipelines should not be interrupted with notices.
	if !ui.Interactive(view) {
		return
	}

	// Skip commands where a notice would be noise or interfere with output.
	if skipUpdateCheck(kctx.Command()) {
		return
	}

	// Without a known build commit there is nothing to compare against.
	if currentCommit() == "" {
		return
	}

	// Bound the check so a slow network never delays the command.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	status, err := _newUpdateChecker(log).Check(ctx)
	if err != nil {
		log.Debug("Update check failed", "error", err)
		return
	}
	if status == nil || !status.Behind {
		return
	}

	log.Warnf("A newer gritt-spice is available (%d commits behind):", status.BehindBy)
	for _, line := range status.CommitLines(5) {
		log.Warn(line)
	}
	log.Warn("Run 'gs update' to upgrade.")
}

// skipUpdateCheck reports whether the update notice
// should be suppressed for the selected command.
//
// command is the resolved command path, e.g. "branch create <name>".
func skipUpdateCheck(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return true
	}

	switch fields[0] {
	case "version", "update", "internal", "shell", "dumpmd":
		return true
	default:
		return false
	}
}

// updateCmd updates gritt-spice to the latest commit on main
// by rebuilding it from source.
type updateCmd struct {
	Force bool `help:"Update even if already up to date."`
}

func (cmd *updateCmd) Run(ctx context.Context, log *silog.Logger) error {
	if currentCommit() == "" {
		return fmt.Errorf("cannot determine the installed version; " +
			"reinstall using install.sh to enable updates")
	}

	checker := _newUpdateChecker(log)

	status, err := checker.Check(ctx)
	if err != nil {
		if !cmd.Force {
			return fmt.Errorf("check for updates: %w", err)
		}
		log.Warn("Could not check for updates; updating anyway", "error", err)
	}

	switch {
	case status != nil && status.Behind:
		log.Infof("Updating gritt-spice (%d commits behind):", status.BehindBy)
		for _, line := range status.CommitLines(0) {
			log.Info(line)
		}
	case status != nil && !cmd.Force:
		log.Info("gritt-spice is already up to date.")
		return nil
	}

	if err := checker.SelfUpdate(ctx); err != nil {
		return fmt.Errorf("update: %w", err)
	}

	log.Info("gritt-spice updated successfully. " +
		"Restart your shell or re-run gs to use the new version.")
	return nil
}
