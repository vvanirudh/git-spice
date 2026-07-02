package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/silog"
)

func TestChecker_Check(t *testing.T) {
	tests := []struct {
		name         string
		response     compareResponse
		wantBehind   bool
		wantBehindBy int
		wantCommits  []CommitSummary
	}{
		{
			name: "Behind",
			response: compareResponse{
				Status:   "behind",
				BehindBy: 2,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
					} `json:"commit"`
				}{
					newCommit("1111111abc", "Add feature A"),
					newCommit("2222222def", "Fix bug B\n\nWith details."),
				},
			},
			wantBehind:   true,
			wantBehindBy: 2,
			wantCommits: []CommitSummary{
				{ShortSHA: "1111111", Subject: "Add feature A"},
				{ShortSHA: "2222222", Subject: "Fix bug B"},
			},
		},
		{
			name:         "Identical",
			response:     compareResponse{Status: "identical", BehindBy: 0},
			wantBehind:   false,
			wantBehindBy: 0,
		},
		{
			name: "Diverged",
			response: compareResponse{
				Status:   "diverged",
				BehindBy: 1,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
					} `json:"commit"`
				}{
					newCommit("3333333aaa", "Upstream change"),
				},
			},
			wantBehind:   true,
			wantBehindBy: 1,
			wantCommits: []CommitSummary{
				{ShortSHA: "3333333", Subject: "Upstream change"},
			},
		},
		{
			name:         "Ahead",
			response:     compareResponse{Status: "ahead", BehindBy: 0},
			wantBehind:   false,
			wantBehindBy: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					require.NoError(t, json.NewEncoder(w).Encode(tt.response))
				},
			))
			defer srv.Close()

			status, err := (&Checker{
				CurrentCommit: "abcdef0",
				APIBaseURL:    srv.URL,
				StateDir:      t.TempDir(),
				Log:           silog.Nop(),
			}).Check(t.Context())
			require.NoError(t, err)

			assert.Equal(t, tt.wantBehind, status.Behind)
			assert.Equal(t, tt.wantBehindBy, status.BehindBy)
			assert.Equal(t, tt.wantCommits, status.Commits)
		})
	}
}

func TestChecker_Check_cacheHonored(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(compareResponse{
				Status:   "behind",
				BehindBy: 1,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
					} `json:"commit"`
				}{newCommit("abc1234def", "New commit")},
			}))
		},
	))
	defer srv.Close()

	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	newChecker := func() *Checker {
		return &Checker{
			CurrentCommit: "old0000",
			APIBaseURL:    srv.URL,
			StateDir:      t.TempDir(),
			Now:           func() time.Time { return now },
			Log:           silog.Nop(),
		}
	}

	// Share a state dir across both checkers.
	stateDir := t.TempDir()

	c1 := newChecker()
	c1.StateDir = stateDir
	_, err := c1.Check(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load(), "first check should hit the network")

	// A second check within the interval must use the cache.
	c2 := newChecker()
	c2.StateDir = stateDir
	status, err := c2.Check(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load(), "cached check should not hit the network")
	assert.True(t, status.Behind)
	assert.Equal(t, "New commit", status.Commits[0].Subject)

	// Once the interval elapses, it refetches.
	c3 := newChecker()
	c3.StateDir = stateDir
	c3.Now = func() time.Time { return now.Add(_checkInterval + time.Hour) }
	_, err = c3.Check(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load(), "stale cache should refetch")
}

func TestChecker_Check_cacheInvalidatedOnNewCommit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(
				compareResponse{Status: "identical"},
			))
		},
	))
	defer srv.Close()

	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()

	_, err := (&Checker{
		CurrentCommit: "commitA",
		APIBaseURL:    srv.URL,
		StateDir:      stateDir,
		Now:           func() time.Time { return now },
		Log:           silog.Nop(),
	}).Check(t.Context())
	require.NoError(t, err)
	require.Equal(t, int32(1), hits.Load())

	// A binary built from a different commit must not trust the old cache.
	_, err = (&Checker{
		CurrentCommit: "commitB",
		APIBaseURL:    srv.URL,
		StateDir:      stateDir,
		Now:           func() time.Time { return now },
		Log:           silog.Nop(),
	}).Check(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load(),
		"changed installed commit should invalidate the cache")
}

func TestChecker_Check_writesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(
				compareResponse{Status: "behind", BehindBy: 3},
			))
		},
	))
	defer srv.Close()

	stateDir := t.TempDir()
	_, err := (&Checker{
		CurrentCommit: "abc",
		APIBaseURL:    srv.URL,
		StateDir:      stateDir,
		Log:           silog.Nop(),
	}).Check(t.Context())
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(stateDir, _cacheFileName))
	require.NoError(t, err)

	var cd cacheData
	require.NoError(t, json.Unmarshal(data, &cd))
	assert.Equal(t, "abc", cd.CurrentCommit)
	assert.Equal(t, 3, cd.BehindBy)
}

func TestChecker_Check_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	))
	defer srv.Close()

	_, err := (&Checker{
		CurrentCommit: "abc",
		APIBaseURL:    srv.URL,
		StateDir:      t.TempDir(),
		Log:           silog.Nop(),
	}).Check(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestStatus_CommitLines(t *testing.T) {
	status := &Status{
		Commits: []CommitSummary{
			{ShortSHA: "aaaaaaa", Subject: "First"},
			{ShortSHA: "bbbbbbb", Subject: "Second"},
			{ShortSHA: "ccccccc", Subject: "Third"},
		},
	}

	t.Run("NoLimit", func(t *testing.T) {
		assert.Equal(t, []string{
			"  aaaaaaa First",
			"  bbbbbbb Second",
			"  ccccccc Third",
		}, status.CommitLines(0))
	})

	t.Run("Truncated", func(t *testing.T) {
		assert.Equal(t, []string{
			"  aaaaaaa First",
			"  bbbbbbb Second",
			"  …and 1 more",
		}, status.CommitLines(2))
	})
}

func newCommit(sha, message string) struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
} {
	var c struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	c.SHA = sha
	c.Commit.Message = message
	return c
}
