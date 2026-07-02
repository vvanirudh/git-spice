package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/alecthomas/kong"
)

type versionFlag bool

func (v versionFlag) BeforeReset(app *kong.Kong) error {
	if err := new(versionCmd).Run(app); err != nil {
		return err
	}

	app.Exit(0)
	return nil
}

type versionCmd struct {
	Short bool `help:"Print only the version number."`
}

func (cmd *versionCmd) Run(app *kong.Kong) error {
	if cmd.Short {
		fmt.Fprintln(app.Stdout, _version)
		return nil
	}

	fmt.Fprint(app.Stdout, "gritt-spice ", _version)
	if report := _generateBuildReport(); report != "" {
		fmt.Fprintf(app.Stdout, " (%s)", report)
	}

	fmt.Fprintln(app.Stdout)
	fmt.Fprintln(app.Stdout, "Copyright (C) Abhinav Gupta")
	fmt.Fprintln(app.Stdout, "Copyright (C) Gritt Robotics")
	fmt.Fprintln(app.Stdout, "  <https://github.com/vvanirudh/gritt-spice>")
	fmt.Fprintln(app.Stdout, "This program comes with ABSOLUTELY NO WARRANTY")
	fmt.Fprintln(app.Stdout, "This is free software, and you are welcome to redistribute it")
	fmt.Fprintln(app.Stdout, "under certain conditions; see source for details.")

	return nil
}

var _debugReadBuildInfo = debug.ReadBuildInfo

// _buildRevision returns the VCS revision embedded at build time
// by the Go toolchain, and whether the working tree was dirty.
// It returns an empty revision if build info is unavailable.
func _buildRevision() (revision string, dirty bool) {
	info, ok := _debugReadBuildInfo()
	if !ok {
		return "", false
	}

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return revision, dirty
}

// currentCommit returns the git commit the running binary was built from.
//
// It prefers the value stamped via ldflags (main._commit, set by install.sh),
// and falls back to the VCS revision embedded by the Go toolchain.
// It returns an empty string when neither is available,
// e.g. for release builds made with -trimpath.
func currentCommit() string {
	if _commit != "" {
		return _commit
	}
	revision, _ := _buildRevision()
	return revision
}

var _generateBuildReport = func() string {
	info, ok := _debugReadBuildInfo()
	if !ok {
		return ""
	}

	var buildTime string
	for _, setting := range info.Settings {
		if setting.Key == "vcs.time" {
			buildTime = setting.Value
		}
	}
	revision, dirty := _buildRevision()

	var out strings.Builder
	if revision != "" {
		out.WriteString(revision)
		if dirty {
			out.WriteString("-dirty")
		}
	}
	if buildTime != "" {
		if out.Len() > 0 {
			fmt.Fprint(&out, " ")
		}
		out.WriteString(buildTime)
	}

	return out.String()
}
