package software_development_test

import (
	"os"
	"strings"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// TestPullRequestReviewLiveReadsTheBranchPair is the smoke test for the wiring a
// stub cannot vouch for: the real API base URL, the credential, the repository
// resolution (GIT_REPO_URL → owner/name) and the head/base filter.
//
// It asks about a branch pair that cannot have an open pull request, so it only
// ever reads — the refusal it expects arrives before any merge call, which is
// also why it is safe to run against a real repository. Follows the CLINE_LIVE /
// CURSOR_LIVE convention: set GITHUB_LIVE=1 (plus a credential, and GIT_REPO_URL
// or GITHUB_REPOSITORY) to run it.
func TestPullRequestReviewLiveReadsTheBranchPair(t *testing.T) {
	if os.Getenv("GITHUB_LIVE") != "1" {
		t.Skip("set GITHUB_LIVE=1 to run")
	}
	if liveEnv("GIT_REPO_URL", "GITHUB_REPOSITORY") == "" {
		t.Skip("set GIT_REPO_URL or GITHUB_REPOSITORY to run")
	}
	if liveEnv("GITHUB_TOKEN", "GH_TOKEN") == "" {
		t.Skip("set GITHUB_TOKEN to run")
	}
	// No PR_BASE_BRANCH on purpose: the trunk then comes from the repository
	// itself, which is one more real call the smoke test covers.
	t.Setenv("PR_BASE_BRANCH", "")

	_, err := (sd.PullRequestReview{}).Run(map[string]string{"from": "no-such-branch-for-the-live-smoke-test"})
	if err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("err=%v want a not_found refusal from the real API for a branch pair that cannot exist", err)
	}
}

// liveEnv returns the first of the names that is set to a non-empty value.
func liveEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}
