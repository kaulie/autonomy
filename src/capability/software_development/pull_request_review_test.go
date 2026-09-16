package software_development_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// ghStub stands in for GitHub's REST API: it records every call the capability
// makes and answers each route with what the test configured, so a test can
// assert both halves — what was asked, and what the gates did with the answer.
type ghStub struct {
	repoStatus      int
	repoBody        string
	pullsStatus     int
	pullsBody       string
	checkRunsStatus int
	checkRunsBody   string
	statusStatus    int
	statusBody      string
	mergeStatus     int
	mergeBody       string

	mu       sync.Mutex
	requests []ghRequest
}

// ghRequest is one recorded call.
type ghRequest struct {
	method string
	path   string
	query  url.Values
	auth   string
	body   map[string]string
}

// newGitHubStub answers like a healthy repository: one open, mergeable pull
// request whose single check passed and whose merge succeeds.
func newGitHubStub() *ghStub {
	return &ghStub{
		repoBody:      `{"full_name":"kaulie/autonomy","default_branch":"main"}`,
		pullsBody:     `[{"number":70,"html_url":"https://github.com/kaulie/autonomy/pull/70","title":"a change","draft":false,"mergeable":true,"mergeable_state":"clean","head":{"ref":"feature/x","sha":"head1sha"},"base":{"ref":"main"}}]`,
		checkRunsBody: `{"total_count":1,"check_runs":[{"name":"ci","status":"completed","conclusion":"success"}]}`,
		statusBody:    `{"state":"success","total_count":0}`,
		mergeBody:     `{"sha":"merge1sha","merged":true,"message":"Pull request successfully merged"}`,
	}
}

func (s *ghStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := ghRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &req.body)
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		status, body := s.answer(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// answer routes by the same paths the capability calls. An unset status is 200
// and an unset body is an empty JSON object.
func (s *ghStub) answer(path string) (int, string) {
	switch {
	case strings.HasSuffix(path, "/check-runs"):
		return statusOr(s.checkRunsStatus), bodyOr(s.checkRunsBody)
	case strings.HasSuffix(path, "/status"):
		return statusOr(s.statusStatus), bodyOr(s.statusBody)
	case strings.HasSuffix(path, "/merge"):
		return statusOr(s.mergeStatus), bodyOr(s.mergeBody)
	case strings.HasSuffix(path, "/pulls"):
		return statusOr(s.pullsStatus), bodyOr(s.pullsBody)
	default:
		return statusOr(s.repoStatus), bodyOr(s.repoBody)
	}
}

func statusOr(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}

func bodyOr(body string) string {
	if strings.TrimSpace(body) == "" {
		return "{}"
	}
	return body
}

// calls returns the recorded calls to paths ending in suffix.
func (s *ghStub) calls(method, suffix string) []ghRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ghRequest
	for _, req := range s.requests {
		if req.method == method && strings.HasSuffix(req.path, suffix) {
			out = append(out, req)
		}
	}
	return out
}

// review runs the capability against the stub with the environment a task
// workspace carries, so a test only states what it is about.
func review(t *testing.T, stub *ghStub, in map[string]string) (map[string]string, error) {
	t.Helper()
	srv := stub.start(t)
	t.Setenv("GITHUB_REPOSITORY", "kaulie/autonomy")
	t.Setenv("GIT_REPO_URL", "")
	t.Setenv("PR_BASE_BRANCH", "")
	return sd.PullRequestReview{APIURL: srv.URL, Token: "test-token", HTTPClient: srv.Client()}.Run(in)
}

// TestPullRequestReviewMergesTheBranchPair pins what the capability is for: the
// open pull request from `from` into `to` is merged, the merge is pinned to the
// head commit that was just checked, and the answer names the merge commit.
func TestPullRequestReviewMergesTheBranchPair(t *testing.T) {
	stub := newGitHubStub()
	out, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	pulls := stub.calls(http.MethodGet, "/pulls")
	if len(pulls) != 1 {
		t.Fatalf("pull lookups=%d want 1 (%+v)", len(pulls), stub.requests)
	}
	// The branch pair is what identifies the pull request, so it is what the
	// lookup filters on — head as owner:branch, plus the open state.
	for param, want := range map[string]string{"head": "kaulie:feature/x", "base": "main", "state": "open"} {
		if got := pulls[0].query.Get(param); got != want {
			t.Errorf("pull lookup %s=%q want %q", param, got, want)
		}
	}

	merges := stub.calls(http.MethodPut, "/merge")
	if len(merges) != 1 {
		t.Fatalf("merge calls=%d want 1 (%+v)", len(merges), stub.requests)
	}
	if got, want := merges[0].path, "/repos/kaulie/autonomy/pulls/70/merge"; got != want {
		t.Errorf("merge path=%q want %q", got, want)
	}
	if got := merges[0].body["merge_method"]; got != "merge" {
		t.Errorf("merge_method=%q want merge (the default)", got)
	}
	// The commit that was checked is the commit GitHub is told to merge: a branch
	// that moved in between is refused by GitHub, not merged unlooked-at.
	if got := merges[0].body["sha"]; got != "head1sha" {
		t.Errorf("merge sha=%q want the checked head commit head1sha", got)
	}
	if got, want := merges[0].auth, "Bearer test-token"; got != want {
		t.Errorf("Authorization=%q want %q", got, want)
	}

	want := map[string]string{
		"from":   "feature/x",
		"to":     "main",
		"number": "70",
		"pr":     "https://github.com/kaulie/autonomy/pull/70",
		"merged": "true",
		"method": "merge",
		"sha":    "merge1sha",
		"checks": "passed",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q]=%q want %q", k, out[k], v)
		}
	}
}

// TestPullRequestReviewDefaultsToTheTrunk: an empty `to` means the trunk, and the
// trunk is what the repository says it is — not a branch name hard-coded here.
func TestPullRequestReviewDefaultsToTheTrunk(t *testing.T) {
	stub := newGitHubStub()
	stub.repoBody = `{"full_name":"kaulie/autonomy","default_branch":"trunk"}`
	if _, err := review(t, stub, map[string]string{"from": "feature/x"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pulls := stub.calls(http.MethodGet, "/pulls")
	if len(pulls) != 1 {
		t.Fatalf("pull lookups=%d want 1", len(pulls))
	}
	if got := pulls[0].query.Get("base"); got != "trunk" {
		t.Errorf("base=%q want the repository default branch trunk", got)
	}
}

// TestPullRequestReviewBaseBranchPrecedence: the caller's `to` wins, then
// PR_BASE_BRANCH, then the repository, then main. The environment beats the
// repository so a host can pin the trunk without asking GitHub first.
func TestPullRequestReviewBaseBranchPrecedence(t *testing.T) {
	stub := newGitHubStub()
	stub.repoBody = `{"full_name":"kaulie/autonomy","default_branch":"trunk"}`

	srv := stub.start(t)
	t.Setenv("GITHUB_REPOSITORY", "kaulie/autonomy")
	t.Setenv("GIT_REPO_URL", "")
	t.Setenv("PR_BASE_BRANCH", "stable")
	c := sd.PullRequestReview{APIURL: srv.URL, Token: "test-token", HTTPClient: srv.Client()}

	// The environment decides when the input is silent...
	if _, err := c.Run(map[string]string{"from": "feature/x"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// ...and the input decides over the environment.
	if _, err := c.Run(map[string]string{"from": "feature/x", "to": "main"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pulls := stub.calls(http.MethodGet, "/pulls")
	if len(pulls) != 2 {
		t.Fatalf("pull lookups=%d want 2", len(pulls))
	}
	for i, want := range []string{"stable", "main"} {
		if got := pulls[i].query.Get("base"); got != want {
			t.Errorf("lookup %d base=%q want %q", i, got, want)
		}
	}
}

// TestPullRequestReviewHonoursTheMergeMethod: the method is the caller's choice,
// and squash really reaches GitHub as squash.
func TestPullRequestReviewHonoursTheMergeMethod(t *testing.T) {
	stub := newGitHubStub()
	out, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main", "method": "squash"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	merges := stub.calls(http.MethodPut, "/merge")
	if len(merges) != 1 || merges[0].body["merge_method"] != "squash" {
		t.Fatalf("merge calls=%+v want one squash merge", merges)
	}
	if out["method"] != "squash" {
		t.Errorf("out[method]=%q want squash", out["method"])
	}
}

// pullJSON renders a one-pull-request list payload with the state under test.
// `mergeable` is raw JSON so a test can pass the null GitHub returns while it is
// still computing mergeability.
func pullJSON(draft bool, mergeable, mergeableState string) string {
	return fmt.Sprintf(`[{"number":70,"html_url":"https://github.com/kaulie/autonomy/pull/70","title":"a change","draft":%t,"mergeable":%s,"mergeable_state":%q,"head":{"ref":"feature/x","sha":"head1sha"},"base":{"ref":"main"}}]`,
		draft, mergeable, mergeableState)
}

// refuses runs the capability expecting a refusal, and insists that a refusal is
// a refusal: nothing reached the merge endpoint.
func refuses(t *testing.T, stub *ghStub, in map[string]string, wantSubstrings ...string) {
	t.Helper()
	out, err := review(t, stub, in)
	if err == nil {
		t.Fatalf("Run merged (%v); want a refusal", out)
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if merges := stub.calls(http.MethodPut, "/merge"); len(merges) != 0 {
		t.Errorf("a pull request that failed a gate was merged anyway: %+v", merges)
	}
}

// TestPullRequestReviewRefusesWhatItMustNotMerge is the review half of the
// capability: each state that must not be merged comes back as its own reason
// instead of as a merge.
func TestPullRequestReviewRefusesWhatItMustNotMerge(t *testing.T) {
	cases := []struct {
		name  string
		pulls string
		want  string
	}{
		{"no pull request for the branch pair", `[]`, "not_found"},
		{"a draft", pullJSON(true, `true`, "clean"), "draft"},
		{"a conflict", pullJSON(false, `false`, "dirty"), "conflict"},
		{"branch protection", pullJSON(false, `true`, "blocked"), "blocked"},
		// A conflict can also be reported by the state alone, with mergeable not
		// yet computed.
		{"a conflict without mergeable", pullJSON(false, `null`, "dirty"), "conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub()
			stub.pullsBody = tc.pulls
			refuses(t, stub, map[string]string{"from": "feature/x", "to": "main"}, tc.want)
		})
	}
}

// TestPullRequestReviewMergesWhileMergeabilityIsUnknown: GitHub computes
// `mergeable` asynchronously, so a null means "not known yet" and must not be
// read as a conflict — the merge is attempted and GitHub itself refuses if there
// really is one.
func TestPullRequestReviewMergesWhileMergeabilityIsUnknown(t *testing.T) {
	stub := newGitHubStub()
	stub.pullsBody = pullJSON(false, `null`, "unknown")
	if _, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if merges := stub.calls(http.MethodPut, "/merge"); len(merges) != 1 {
		t.Fatalf("merge calls=%d want 1", len(merges))
	}
}

// TestPullRequestReviewWaitsForChecksThatHaveNotFinished: a running check is not
// green. The refusal says so, so the planner asks again on a later cycle instead
// of this call blocking on the CI.
func TestPullRequestReviewWaitsForChecksThatHaveNotFinished(t *testing.T) {
	stub := newGitHubStub()
	stub.checkRunsBody = `{"total_count":1,"check_runs":[{"name":"build","status":"in_progress","conclusion":""}]}`
	refuses(t, stub, map[string]string{"from": "feature/x", "to": "main"}, "checks_pending", "build")
}

// TestPullRequestReviewRefusesRedChecks: a failing check names itself, so the
// planner knows what to look at.
func TestPullRequestReviewRefusesRedChecks(t *testing.T) {
	stub := newGitHubStub()
	stub.checkRunsBody = `{"total_count":2,"check_runs":[` +
		`{"name":"build","status":"completed","conclusion":"failure"},` +
		`{"name":"lint","status":"completed","conclusion":"success"}]}`
	refuses(t, stub, map[string]string{"from": "feature/x", "to": "main"}, "checks_failed", "build (failure)")
}

// TestPullRequestReviewGatesOnTheLegacyCommitStatus: not every repository posts
// through the Checks API, so the combined commit status is read too.
func TestPullRequestReviewGatesOnTheLegacyCommitStatus(t *testing.T) {
	t.Run("failure refuses", func(t *testing.T) {
		stub := newGitHubStub()
		stub.checkRunsBody = `{"total_count":0,"check_runs":[]}`
		stub.statusBody = `{"state":"failure","total_count":1}`
		refuses(t, stub, map[string]string{"from": "feature/x", "to": "main"}, "checks_failed")
	})
	t.Run("pending refuses", func(t *testing.T) {
		stub := newGitHubStub()
		stub.checkRunsBody = `{"total_count":0,"check_runs":[]}`
		stub.statusBody = `{"state":"pending","total_count":1}`
		refuses(t, stub, map[string]string{"from": "feature/x", "to": "main"}, "checks_pending")
	})
}

// TestPullRequestReviewMergesARepositoryWithoutChecks: no CI configured is not a
// failure. Nothing is waiting to be satisfied, and the answer says so ("none")
// rather than pretending checks passed.
func TestPullRequestReviewMergesARepositoryWithoutChecks(t *testing.T) {
	stub := newGitHubStub()
	stub.checkRunsBody = `{"total_count":0,"check_runs":[]}`
	// The combined status endpoint reports pending when there are no statuses at
	// all — the count is what says there is nothing to judge.
	stub.statusBody = `{"state":"pending","total_count":0}`
	out, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["checks"] != "none" {
		t.Errorf("out[checks]=%q want none", out["checks"])
	}
}

// TestPullRequestReviewReportsTheGitHostsRefusal: when GitHub refuses the merge,
// its own reason reaches the caller instead of only a status code.
func TestPullRequestReviewReportsTheGitHostsRefusal(t *testing.T) {
	stub := newGitHubStub()
	stub.mergeStatus = http.StatusMethodNotAllowed
	stub.mergeBody = `{"message":"Pull Request is not mergeable"}`
	_, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err == nil {
		t.Fatal("Run succeeded; want GitHub's refusal")
	}
	for _, want := range []string{"HTTP 405", "not mergeable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// TestPullRequestReviewDoesNotCallAnUnmergedSuccess: a 2xx that does not claim a
// merge is not a merge, so it is reported as a failure.
func TestPullRequestReviewDoesNotCallAnUnmergedSuccess(t *testing.T) {
	stub := newGitHubStub()
	stub.mergeBody = `{"merged":false,"message":"Pull Request is not mergeable"}`
	_, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err == nil || !strings.Contains(err.Error(), "without merging") {
		t.Fatalf("err=%v want a 'without merging' failure", err)
	}
}

// TestPullRequestReviewReadsTheRepositoryOutOfTheGitRemote: a task workspace
// knows its project as a git remote, and that is enough to name the repository.
func TestPullRequestReviewReadsTheRepositoryOutOfTheGitRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/kaulie/autonomy.git",
		"git@github.com:kaulie/autonomy.git",
		"https://github.com/kaulie/autonomy",
	} {
		t.Run(remote, func(t *testing.T) {
			stub := newGitHubStub()
			srv := stub.start(t)
			t.Setenv("GITHUB_REPOSITORY", "")
			t.Setenv("PR_BASE_BRANCH", "")
			t.Setenv("GIT_REPO_URL", remote)
			c := sd.PullRequestReview{APIURL: srv.URL, Token: "test-token", HTTPClient: srv.Client()}
			if _, err := c.Run(map[string]string{"from": "feature/x", "to": "main"}); err != nil {
				t.Fatalf("Run with %s: %v", remote, err)
			}
			merges := stub.calls(http.MethodPut, "/merge")
			if len(merges) != 1 || merges[0].path != "/repos/kaulie/autonomy/pulls/70/merge" {
				t.Fatalf("merge calls=%+v want one at /repos/kaulie/autonomy/pulls/70/merge", merges)
			}
		})
	}
}

// TestPullRequestReviewNeedsWhatItCannotGuess covers the inputs it refuses to
// invent — the head branch, the repository, the credential — and the merge
// methods it knows. All of these fail before a single call is made.
func TestPullRequestReviewNeedsWhatItCannotGuess(t *testing.T) {
	cases := []struct {
		name  string
		in    map[string]string
		repo  string
		token string
		want  string
	}{
		{"no from", map[string]string{"to": "main"}, "kaulie/autonomy", "t", "missing from"},
		{"no repo", map[string]string{"from": "feature/x"}, "", "t", "missing repository"},
		{"no credential", map[string]string{"from": "feature/x"}, "kaulie/autonomy", "", "no credential"},
		{"unknown method", map[string]string{"from": "feature/x", "method": "fast-forward"}, "kaulie/autonomy", "t", "unknown method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", "")
			t.Setenv("GIT_REPO_URL", "")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("PR_BASE_BRANCH", "")
			_, err := (sd.PullRequestReview{APIURL: "http://127.0.0.1:1", Token: tc.token, Repo: tc.repo}).Run(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want it to mention %q", err, tc.want)
			}
		})
	}
}
