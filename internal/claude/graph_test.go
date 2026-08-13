package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleImpactJSON mirrors the real output of
// `code-review-graph detect-changes --base <ref>` (full JSON mode),
// trimmed to two changed functions with one test gap.
const sampleImpactJSON = `{
  "summary": "Analyzed 2 changed file(s):\n  - 2 changed function(s)/class(es)\n  - Overall risk score: 0.50",
  "risk_score": 0.5,
  "changed_functions": [
    {"name": "executeSplit", "qualified_name": "/repo/a.go::C.executeSplit", "file_path": "/repo/a.go", "line_start": 512, "line_end": 694, "risk_score": 0.5, "is_test": false},
    {"name": "helper", "qualified_name": "/repo/b.go::helper", "file_path": "/repo/b.go", "line_start": 1, "line_end": 5, "risk_score": 0.1, "is_test": false}
  ],
  "affected_flows": [],
  "test_gaps": [
    {"name": "executeSplit", "qualified_name": "/repo/a.go::C.executeSplit", "file": "/repo/a.go", "line_start": 512, "line_end": 694}
  ],
  "review_priorities": [
    {"name": "executeSplit", "qualified_name": "/repo/a.go::C.executeSplit", "file_path": "/repo/a.go", "line_start": 512, "line_end": 694, "risk_score": 0.5, "is_test": false},
    {"name": "helper", "qualified_name": "/repo/b.go::helper", "file_path": "/repo/b.go", "line_start": 1, "line_end": 5, "risk_score": 0.1, "is_test": false}
  ],
  "functions_truncated": false,
  "context_savings": {"estimated": true, "saved_tokens": 9772, "saved_percent": 85}
}`

func TestParseImpact(t *testing.T) {
	impact, err := ParseImpact([]byte(sampleImpactJSON))
	require.NoError(t, err)

	assert.Equal(t, 0.5, impact.RiskScore)
	require.Len(t, impact.ChangedFunctions, 2)
	assert.Equal(t, "executeSplit", impact.ChangedFunctions[0].Name)
	require.Len(t, impact.TestGaps, 1)
	assert.Equal(t, "/repo/a.go", impact.TestGaps[0].File)
	require.NotNil(t, impact.ContextSavings)
	assert.Equal(t, 85, impact.ContextSavings.SavedPercent)
}

func TestParseImpact_invalid(t *testing.T) {
	_, err := ParseImpact([]byte("not json"))
	assert.Error(t, err)
}

func TestImpact_FormatContext(t *testing.T) {
	impact, err := ParseImpact([]byte(sampleImpactJSON))
	require.NoError(t, err)

	got := impact.FormatContext("/repo")

	// Header, summary line, risk-sorted priorities with relativized
	// paths, and the untested flag.
	assert.Contains(t, got, "## Impact analysis (code-review-graph)")
	assert.Contains(t, got, "Overall risk: 0.50")
	assert.Contains(t, got, "2 changed functions")
	assert.Contains(t, got, "1 untested")
	assert.Contains(t, got, "executeSplit (a.go:512-694) risk 0.50 — UNTESTED")
	assert.Contains(t, got, "helper (b.go:1-5) risk 0.10")

	// Absolute paths must not leak into the prompt.
	assert.NotContains(t, got, "/repo/a.go")
}

func TestImpact_FormatContext_empty(t *testing.T) {
	// No changed functions => nothing worth injecting.
	assert.Empty(t, (&Impact{}).FormatContext("/repo"))
}

func TestGraphClient_IsAvailable(t *testing.T) {
	// An explicit binary path is trusted without a PATH lookup.
	assert.True(t, NewGraphClient("/usr/bin/true", nil).IsAvailable())
}
