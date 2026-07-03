package spice

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog/silogtest"
	"go.abhg.dev/gs/internal/spice/state"
	"go.uber.org/mock/gomock"
)

// Regression test for a bug where merge-based restack
// skipped the merge and reported success
// when the recorded base hash was already up to date
// (equal to the base branch's current head)
// but the branch head did not yet contain that commit,
// and no fork point was available.
//
// In that state, the upstream guess resolves to the base hash itself,
// so a fast-forward check against the upstream is trivially true.
// The check must be made against the branch head instead.
func TestService_Restack_mergeBaseHashUpToDateHeadBehind(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	ctx := t.Context()

	baseHash := git.Hash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	headHash := git.Hash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	// Track "feature" on "main" with the recorded base hash
	// already matching main's current head.
	store := NewMemoryStore(t)
	tx := store.BeginBranchTx()
	require.NoError(t, tx.Upsert(ctx, state.UpsertRequest{
		Name:     "feature",
		Base:     "main",
		BaseHash: baseHash,
	}))
	require.NoError(t, tx.Commit(ctx, "track feature"))

	mockRepo := NewMockGitRepository(mockCtrl)
	mockRepo.EXPECT().
		PeelToCommit(gomock.Any(), "feature").
		Return(headHash, nil).
		AnyTimes()
	mockRepo.EXPECT().
		PeelToCommit(gomock.Any(), "main").
		Return(baseHash, nil).
		AnyTimes()
	// The branch head does not contain the base commit.
	mockRepo.EXPECT().
		IsAncestor(gomock.Any(), baseHash, headHash).
		Return(false).
		AnyTimes()
	// A commit is always an ancestor of itself.
	mockRepo.EXPECT().
		IsAncestor(gomock.Any(), baseHash, baseHash).
		Return(true).
		AnyTimes()
	// No fork point is available
	// (e.g. fresh clone or expired reflog).
	mockRepo.EXPECT().
		ForkPoint(gomock.Any(), "main", "feature").
		Return(git.ZeroHash, errors.New("no fork point")).
		AnyTimes()

	mockWorktree := NewMockGitWorktree(mockCtrl)
	mockWorktree.EXPECT().
		CurrentBranch(gomock.Any()).
		Return("feature", nil)
	// The merge MUST take place: this is the core of the regression.
	mockWorktree.EXPECT().
		Merge(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, req git.MergeRequest) error {
			assert.Equal(t, baseHash.String(), req.Commit)
			return nil
		})

	svc := newService(
		mockRepo, mockWorktree, store, nil,
		silogtest.New(t), RestackMethodMerge,
	)

	resp, err := svc.Restack(ctx, "feature")
	require.NoError(t, err)
	assert.Equal(t, "main", resp.Base)
}
