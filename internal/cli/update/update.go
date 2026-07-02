// Package update implements gritt-spice self-update:
// detecting when the installed binary is behind
// the latest commit on the fork's main branch,
// and rebuilding it from source.
//
// gritt-spice is installed from source
// (see install.sh, which runs "go install go.abhg.dev/gs"),
// so staleness is measured by comparing the commit
// the binary was built from against the tip of main
// using GitHub's REST compare API.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec"
)

// Defaults identifying the gritt-spice fork.
//
// These are declared as vars rather than consts
// so that tests may override them.
var (
	DefaultOwner  = "vvanirudh"
	DefaultRepo   = "gritt-spice"
	DefaultBranch = "main"

	// DefaultAPIBaseURL is the base URL for the GitHub REST API.
	DefaultAPIBaseURL = "https://api.github.com"
)

// _modulePath is the Go module path installed by "go install".
// It differs from the fork's GitHub path,
// so "go install ...@latest" cannot target the fork;
// SelfUpdate clones the fork and builds it in place instead.
const _modulePath = "go.abhg.dev/gs"

// _checkInterval is how often we hit the network to check for updates.
// Between checks, the cached result is reused.
const _checkInterval = 24 * time.Hour

// _cacheFileName is the name of the cache file under the state directory.
const _cacheFileName = "update-check.json"

// _httpTimeout bounds the update check so it never hangs a command.
const _httpTimeout = 3 * time.Second

// Checker detects and applies gritt-spice updates.
type Checker struct {
	// Owner, Repo, and Branch identify the fork to check against.
	// If empty, the Default* values are used.
	Owner, Repo, Branch string

	// CurrentCommit is the commit the running binary was built from.
	CurrentCommit string

	// APIBaseURL is the base URL for the GitHub REST API.
	// Defaults to DefaultAPIBaseURL.
	APIBaseURL string

	// Token, if set, is sent as a bearer token
	// to raise the unauthenticated rate limit.
	Token string

	// HTTP is the client used for API requests.
	// Defaults to a client with a short timeout.
	HTTP *http.Client

	// StateDir is the directory in which the check result is cached.
	StateDir string

	// Now reports the current time. Defaults to time.Now.
	Now func() time.Time

	Log *silog.Logger
}

// CommitSummary is a one-line description of a commit.
type CommitSummary struct {
	// ShortSHA is the abbreviated commit hash.
	ShortSHA string `json:"sha"`

	// Subject is the first line of the commit message.
	Subject string `json:"subject"`
}

// Status is the result of an update check.
type Status struct {
	// Behind reports whether the installed commit
	// is behind the tip of the branch.
	Behind bool

	// BehindBy is the number of commits the installed commit is behind.
	BehindBy int

	// LatestCommit is the commit at the tip of the branch.
	LatestCommit string

	// Commits summarizes the commits added since the installed commit,
	// newest last.
	Commits []CommitSummary
}

// CommitLines renders the pending commits as human-readable lines,
// each prefixed with two spaces for indentation.
//
// If limit is greater than zero and there are more commits than that,
// only the first limit commits are shown
// followed by an "…and N more" line.
func (s *Status) CommitLines(limit int) []string {
	shown := len(s.Commits)
	if limit > 0 && shown > limit {
		shown = limit
	}

	var lines []string
	for _, c := range s.Commits[:shown] {
		lines = append(lines, fmt.Sprintf("  %s %s", c.ShortSHA, c.Subject))
	}
	if remaining := len(s.Commits) - shown; remaining > 0 {
		lines = append(lines, fmt.Sprintf("  …and %d more", remaining))
	}
	return lines
}

func (c *Checker) owner() string {
	if c.Owner != "" {
		return c.Owner
	}
	return DefaultOwner
}

func (c *Checker) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Checker) branch() string {
	if c.Branch != "" {
		return c.Branch
	}
	return DefaultBranch
}

func (c *Checker) apiBaseURL() string {
	if c.APIBaseURL != "" {
		return c.APIBaseURL
	}
	return DefaultAPIBaseURL
}

func (c *Checker) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: _httpTimeout}
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) log() *silog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return silog.Nop()
}

// Check reports whether the installed commit is behind the branch tip.
//
// It reuses a cached result if the last check was recent,
// avoiding a network call on most invocations.
// When the cache is stale, it queries the GitHub compare API,
// rewrites the cache, and returns the fresh result.
func (c *Checker) Check(ctx context.Context) (*Status, error) {
	now := c.now()

	// Serve from cache when it is fresh and matches the installed commit.
	if cached, ok := c.readCache(); ok &&
		cached.CurrentCommit == c.CurrentCommit &&
		now.Sub(cached.LastCheck) < _checkInterval {
		return cached.toStatus(), nil
	}

	status, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}

	c.writeCache(cacheData{
		LastCheck:     now,
		CurrentCommit: c.CurrentCommit,
		LatestCommit:  status.LatestCommit,
		BehindBy:      status.BehindBy,
		Commits:       status.Commits,
	})

	return status, nil
}

// compareResponse is the subset of GitHub's compare API response we consume.
//
// See https://docs.github.com/en/rest/commits/commits#compare-two-commits.
type compareResponse struct {
	Status   string `json:"status"`
	BehindBy int    `json:"behind_by"`
	Commits  []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
}

func (c *Checker) fetch(ctx context.Context) (*Status, error) {
	// The compare "base...head" range yields the commits
	// present in head (the branch) but not in base (the installed commit),
	// i.e. exactly the commits added since installation.
	url := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s",
		c.apiBaseURL(), c.owner(), c.repo(), c.CurrentCommit, c.branch())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gritt-spice")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		// Read a little of the body for context, but do not fail on it.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("unexpected status %s: %s",
			res.Status, strings.TrimSpace(string(body)))
	}

	var cmp compareResponse
	if err := json.NewDecoder(res.Body).Decode(&cmp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	status := &Status{
		Behind:       cmp.BehindBy > 0,
		BehindBy:     cmp.BehindBy,
		LatestCommit: c.CurrentCommit,
	}
	for _, commit := range cmp.Commits {
		status.Commits = append(status.Commits, CommitSummary{
			ShortSHA: shortSHA(commit.SHA),
			Subject:  subject(commit.Commit.Message),
		})
	}
	// The last commit in the range is the branch tip.
	if n := len(cmp.Commits); n > 0 {
		status.LatestCommit = cmp.Commits[n-1].SHA
	}

	return status, nil
}

// SelfUpdate rebuilds the gs binary from the latest commit on the branch.
//
// It clones the fork into a temporary directory
// and runs "go install" from there,
// stamping the new commit into the binary
// so future update checks are accurate.
// It requires git and go to be available on PATH.
func (c *Checker) SelfUpdate(ctx context.Context) error {
	for _, tool := range []string{"git", "go"} {
		if _, err := xec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required to update: %w", tool, err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "gritt-spice-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	log := c.log()

	// Clone only the tip of the branch to keep the download small.
	cloneURL := fmt.Sprintf("https://github.com/%s/%s", c.owner(), c.repo())
	if err := xec.Command(
		ctx, log,
		"git", "clone", "--depth", "1", "--branch", c.branch(), cloneURL, tmpDir,
	).Run(); err != nil {
		return fmt.Errorf("clone repository: %w", err)
	}

	sha, err := xec.Command(ctx, log, "git", "-C", tmpDir, "rev-parse", "HEAD").
		OutputChomp()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}

	// Build from the local checkout, stamping the commit into the binary.
	if err := xec.Command(
		ctx, log,
		"go", "install", "-ldflags", "-X main._commit="+sha, _modulePath,
	).WithDir(tmpDir).Run(); err != nil {
		return fmt.Errorf("go install: %w", err)
	}

	return nil
}

// cacheData is the on-disk format of the update-check cache.
type cacheData struct {
	LastCheck     time.Time       `json:"last_check"`
	CurrentCommit string          `json:"current_commit"`
	LatestCommit  string          `json:"latest_commit"`
	BehindBy      int             `json:"behind_by"`
	Commits       []CommitSummary `json:"commits"`
}

func (cd cacheData) toStatus() *Status {
	return &Status{
		Behind:       cd.BehindBy > 0,
		BehindBy:     cd.BehindBy,
		LatestCommit: cd.LatestCommit,
		Commits:      cd.Commits,
	}
}

func (c *Checker) cachePath() string {
	return filepath.Join(c.StateDir, _cacheFileName)
}

func (c *Checker) readCache() (cacheData, bool) {
	if c.StateDir == "" {
		return cacheData{}, false
	}

	data, err := os.ReadFile(c.cachePath())
	if err != nil {
		return cacheData{}, false
	}

	var cd cacheData
	if err := json.Unmarshal(data, &cd); err != nil {
		return cacheData{}, false
	}
	return cd, true
}

func (c *Checker) writeCache(cd cacheData) {
	if c.StateDir == "" {
		return
	}

	data, err := json.MarshalIndent(cd, "", "  ")
	if err != nil {
		c.log().Debug("Cannot encode update cache", "error", err)
		return
	}

	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		c.log().Debug("Cannot create state directory", "error", err)
		return
	}

	if err := os.WriteFile(c.cachePath(), data, 0o644); err != nil {
		c.log().Debug("Cannot write update cache", "error", err)
	}
}

// shortSHA abbreviates a commit hash to its first 7 characters.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// subject returns the first line of a commit message.
func subject(message string) string {
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = message[:i]
	}
	return strings.TrimSpace(message)
}
