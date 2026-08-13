package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec"
)

// graphBinaryName is the code-review-graph CLI executable.
const graphBinaryName = "code-review-graph"

// ErrGraphNotInstalled indicates the code-review-graph CLI is not
// available on the system.
var ErrGraphNotInstalled = errors.New("code-review-graph not installed")

// Impact is the structural change analysis produced by
// `code-review-graph detect-changes --base <ref>` in full JSON mode.
//
// It augments a plain diff with call-graph context: which functions
// changed, how risky they are, and which of them lack test coverage.
type Impact struct {
	// Summary is the tool's human-readable one-paragraph summary.
	Summary string `json:"summary"`

	// RiskScore is the overall risk of the change, in [0, 1].
	RiskScore float64 `json:"risk_score"`

	// ChangedFunctions lists every function/class touched by the change.
	ChangedFunctions []ImpactNode `json:"changed_functions"`

	// AffectedFlows lists execution flows impacted by the change.
	// The element shape is opaque; we only report the count.
	AffectedFlows []json.RawMessage `json:"affected_flows"`

	// TestGaps lists changed code with no covering tests.
	TestGaps []ImpactGap `json:"test_gaps"`

	// ReviewPriorities is the tool's ranked "look here first" list.
	ReviewPriorities []ImpactNode `json:"review_priorities"`

	// ContextSavings estimates tokens saved versus sending full files.
	ContextSavings *ContextSavings `json:"context_savings"`
}

// ImpactNode is a single changed or high-priority code entity.
type ImpactNode struct {
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	FilePath      string  `json:"file_path"`
	LineStart     int     `json:"line_start"`
	LineEnd       int     `json:"line_end"`
	RiskScore     float64 `json:"risk_score"`
	IsTest        bool    `json:"is_test"`
}

// ImpactGap is a changed entity that lacks test coverage.
//
// Note the JSON key for the path is "file" here, not "file_path".
type ImpactGap struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	File          string `json:"file"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
}

// ContextSavings estimates the token cost avoided by using graph
// context instead of full file contents.
type ContextSavings struct {
	Estimated    bool `json:"estimated"`
	SavedTokens  int  `json:"saved_tokens"`
	SavedPercent int  `json:"saved_percent"`
}

// maxReviewPriorities bounds how many entries we inject into the prompt
// to keep the added context compact.
const maxReviewPriorities = 10

// ParseImpact decodes the JSON emitted by `detect-changes` (full mode).
func ParseImpact(data []byte) (*Impact, error) {
	var impact Impact
	if err := json.Unmarshal(data, &impact); err != nil {
		return nil, fmt.Errorf("parse impact JSON: %w", err)
	}
	return &impact, nil
}

// FormatContext renders the impact as a compact Markdown block suitable
// for injection into a review prompt. Paths are relativized against
// repoRoot so they line up with the diff's paths.
//
// Returns an empty string when there is nothing worth injecting
// (no changed functions), so callers can inject unconditionally.
func (im *Impact) FormatContext(repoRoot string) string {
	if im == nil || len(im.ChangedFunctions) == 0 {
		return ""
	}

	// Build the set of untested qualified names for quick lookup.
	untested := make(map[string]struct{}, len(im.TestGaps))
	for _, g := range im.TestGaps {
		untested[g.QualifiedName] = struct{}{}
	}

	var b strings.Builder
	b.WriteString("## Impact analysis (code-review-graph)\n")
	fmt.Fprintf(&b, "Overall risk: %.2f · %d changed functions · %d untested\n",
		im.RiskScore, len(im.ChangedFunctions), len(im.TestGaps))

	// Rank priorities by risk (highest first) so the reviewer's
	// attention is drawn to the riskiest code regardless of input order.
	priorities := append([]ImpactNode(nil), im.ReviewPriorities...)
	sort.SliceStable(priorities, func(i, j int) bool {
		return priorities[i].RiskScore > priorities[j].RiskScore
	})

	if len(priorities) > 0 {
		b.WriteString("\n### Review priorities (highest risk first)\n")
		for i, n := range priorities {
			if i >= maxReviewPriorities {
				break
			}
			loc := fmt.Sprintf("%s:%d-%d",
				relPath(repoRoot, n.FilePath), n.LineStart, n.LineEnd)
			fmt.Fprintf(&b, "%d. %s (%s) risk %.2f", i+1, n.Name, loc, n.RiskScore)
			if _, ok := untested[n.QualifiedName]; ok {
				b.WriteString(" — UNTESTED")
			}
			b.WriteByte('\n')
		}
	}

	if len(im.TestGaps) > 0 {
		names := make([]string, 0, len(im.TestGaps))
		for _, g := range im.TestGaps {
			names = append(names, g.Name)
		}
		b.WriteString("\n### Untested changed code\n")
		b.WriteString(strings.Join(names, ", "))
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}

// relPath returns path relative to root when possible, falling back to
// the base name (never an absolute path, which would confuse the model).
func relPath(root, path string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, path); err == nil &&
			!strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(path)
}

// GraphClient shells out to the code-review-graph CLI to compute
// structural change impact. It is optional: when the binary is absent,
// callers fall back to a plain diff review.
type GraphClient struct {
	binaryPath string
	log        *silog.Logger
}

// NewGraphClient creates a GraphClient. If binaryPath is empty, the
// binary is resolved from PATH. A nil logger is replaced with a no-op.
func NewGraphClient(binaryPath string, log *silog.Logger) *GraphClient {
	if log == nil {
		log = silog.Nop()
	}
	return &GraphClient{binaryPath: binaryPath, log: log}
}

// IsAvailable reports whether the code-review-graph CLI can be found.
func (c *GraphClient) IsAvailable() bool {
	_, err := c.resolve()
	return err == nil
}

// resolve returns the path to the code-review-graph binary.
func (c *GraphClient) resolve() (string, error) {
	if c.binaryPath != "" {
		return c.binaryPath, nil
	}
	path, err := xec.LookPath(graphBinaryName)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrGraphNotInstalled, err)
	}
	return path, nil
}

// RunImpact computes the change impact of base...working-tree using the
// existing graph. It does not rebuild the graph; a stale or missing
// graph yields an error, which callers should treat as "no context".
func (c *GraphClient) RunImpact(
	ctx context.Context,
	repoRoot, base string,
) (*Impact, error) {
	bin, err := c.resolve()
	if err != nil {
		return nil, err
	}

	// Bound the external call so a hung graph process never blocks a
	// review; enrichment is best-effort.
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := xec.Command(ctx, c.log, bin,
		"detect-changes", "--base", base, "--repo", repoRoot).Output()
	if err != nil {
		return nil, fmt.Errorf("code-review-graph: %w", err)
	}

	return ParseImpact(out)
}
