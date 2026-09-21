package software_development_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// noGitHubCLI is the gh source for a test that means "there is no credential
// anywhere". The capability's default asks the machine's gh CLI — which on a
// developer machine is usually signed in — so a test about the refusal has to say
// so itself instead of depending on whose gh is logged in.
func noGitHubCLI(context.Context) (string, error) { return "", errors.New("no gh CLI") }

// ghStub stands in for GitHub's REST API: it records every call the capability
// makes and answers each route with what the test configured, so a test can
// assert both halves — what was asked, and what the capability did with the
// answer. It has no merge route on purpose: the capability must never ask for
// one, so a request to /merge is simply recorded (and fails the test that looks).
type ghStub struct {
	repoStatus    int
	repoBody      string
	pullsStatus   int
	pullsBody     string
	pullStatus    int
	pullBody      string
	reviewsStatus int
	reviewsBody   string

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

// newGitHubStub answers like a healthy repository: one open pull request (#70)
// whose two reviewers left an approval and a change request. The branch-pair
// path finds it through the list endpoint and the pull-request path through the
// one-pull-request endpoint, so both read the same pull request, and both read
// the same reviews.
func newGitHubStub() *ghStub {
	return &ghStub{
		repoBody:  `{"full_name":"kaulie/autonomy","default_branch":"main"}`,
		pullsBody: `[{"number":70,"html_url":"https://github.com/kaulie/autonomy/pull/70","title":"a change","state":"open","draft":false,"head":{"ref":"feature/x","sha":"head1sha"},"base":{"ref":"main"}}]`,
		pullBody:  `{"number":70,"html_url":"https://github.com/kaulie/autonomy/pull/70","title":"a change","state":"open","draft":false,"head":{"ref":"feature/x","sha":"head1sha"},"base":{"ref":"main"}}`,
		reviewsBody: `[` +
			`{"id":1,"user":{"login":"alice"},"state":"APPROVED","body":"looks good","submitted_at":"2024-01-01T00:00:00Z","html_url":"https://github.com/kaulie/autonomy/pull/70#pullrequestreview-1"},` +
			`{"id":2,"user":{"login":"bob"},"state":"CHANGES_REQUESTED","body":"please rename","submitted_at":"2024-01-02T00:00:00Z","html_url":"https://github.com/kaulie/autonomy/pull/70#pullrequestreview-2"}` +
			`]`,
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
	case strings.HasSuffix(path, "/reviews"):
		return statusOr(s.reviewsStatus), bodyOr(s.reviewsBody)
	case onePullPath(path):
		return statusOr(s.pullStatus), bodyOr(s.pullBody)
	case strings.HasSuffix(path, "/pulls"):
		return statusOr(s.pullsStatus), bodyOr(s.pullsBody)
	default:
		return statusOr(s.repoStatus), bodyOr(s.repoBody)
	}
}

// onePullPath reports whether path is the one-pull-request endpoint
// (/repos/<owner>/<name>/pulls/<number>) — what a caller that named the pull
// request outright reads, instead of listing by branch pair.
func onePullPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-2] != "pulls" {
		return false
	}
	return pullNumber(parts[len(parts)-1])
}

// pullNumber reports whether s is a pull request number: digits only, never zero.
func pullNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != "0"
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

// reviewsOf decodes the reviews JSON the capability returned.
func reviewsOf(t *testing.T, out map[string]string) []struct {
	Author string `json:"author"`
	State  string `json:"state"`
	Body   string `json:"body"`
} {
	t.Helper()
	var opinions []struct {
		Author string `json:"author"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out["reviews"]), &opinions); err != nil {
		t.Fatalf("reviews=%q is not JSON: %v", out["reviews"], err)
	}
	return opinions
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

// TestPullRequestReviewReturnsTheReviewOpinions pins what the capability is for
// now: the open pull request from `from` into `to` is found, its reviews are
// read, and they are handed back — with merged:"false" and
// requires_human_approval:"true", because the capability does not merge.
func TestPullRequestReviewReturnsTheReviewOpinions(t *testing.T) {
	stub := newGitHubStub()
	out, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// It finds the pull request by the branch pair...
	pulls := stub.calls(http.MethodGet, "/pulls")
	if len(pulls) != 1 {
		t.Fatalf("pull lookups=%d want 1 (%+v)", len(pulls), stub.requests)
	}
	for param, want := range map[string]string{"head": "kaulie:feature/x", "base": "main", "state": "open"} {
		if got := pulls[0].query.Get(param); got != want {
			t.Errorf("pull lookup %s=%q want %q", param, got, want)
		}
	}
	// ...reads its reviews...
	reviews := stub.calls(http.MethodGet, "/reviews")
	if len(reviews) != 1 || reviews[0].path != "/repos/kaulie/autonomy/pulls/70/reviews" {
		t.Fatalf("review calls=%+v want one at /repos/kaulie/autonomy/pulls/70/reviews", reviews)
	}
	// ...and hands them back.
	opinions := reviewsOf(t, out)
	if len(opinions) != 2 {
		t.Fatalf("opinions=%+v want 2", opinions)
	}
	if opinions[0].Author != "alice" || opinions[0].State != "approved" || opinions[0].Body != "looks good" {
		t.Errorf("first opinion=%+v want alice/approved/looks good", opinions[0])
	}
	if opinions[1].Author != "bob" || opinions[1].State != "changes_requested" || opinions[1].Body != "please rename" {
		t.Errorf("second opinion=%+v want bob/changes_requested/please rename", opinions[1])
	}
	if got := out["reviews_count"]; got != "2" {
		t.Errorf("reviews_count=%q want 2", got)
	}
	if got, want := out["review_summary"], "1 approved, 1 changes_requested"; got != want {
		t.Errorf("review_summary=%q want %q", got, want)
	}

	// The whole point: it never merges, and it says so.
	if got := out["merged"]; got != "false" {
		t.Errorf("merged=%q want false", got)
	}
	if got := out["requires_human_approval"]; got != "true" {
		t.Errorf("requires_human_approval=%q want true", got)
	}
	if _, ok := out["sha"]; ok {
		t.Errorf("out=%v, want no merge sha", out)
	}

	// It carries the pull request's own facts back too.
	if out["number"] != "70" || out["from"] != "feature/x" || out["to"] != "main" {
		t.Errorf("out=%v want number 70, feature/x -> main", out)
	}
	if out["state"] != "open" || out["draft"] != "false" {
		t.Errorf("out=%v want state open and draft false", out)
	}
}

// TestPullRequestReviewHasNoMergePath: the capability makes read-only calls. It
// never reaches GitHub's merge endpoint, whatever the pull request looks like —
// there is no merge code path to turn on.
func TestPullRequestReviewHasNoMergePath(t *testing.T) {
	stub := newGitHubStub()
	out, err := review(t, stub, map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/70"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["merged"] != "false" || out["requires_human_approval"] != "true" {
		t.Fatalf("out=%v want merged=false and requires_human_approval=true", out)
	}
	for _, req := range stub.requests {
		if strings.HasSuffix(req.path, "/merge") {
			t.Fatalf("the capability called the merge endpoint: %s %s", req.method, req.path)
		}
		if req.method != http.MethodGet {
			t.Fatalf("the capability made a %s call (%s); it must only read", req.method, req.path)
		}
	}
}

// TestPullRequestReviewRecordsApprovalWithoutActingOnIt: a pull request that
// reviewers have approved comes back with the approval among its reviews, but the
// capability still does not merge it — an approval is an opinion it reports, never
// a decision it takes. Landing stays a human's call, so even a fully-approved
// pull request is reported with merged:"false" and requires_human_approval:"true".
func TestPullRequestReviewRecordsApprovalWithoutActingOnIt(t *testing.T) {
	stub := newGitHubStub()
	stub.reviewsBody = `[{"id":1,"user":{"login":"alice"},"state":"APPROVED","body":"ship it"}]`
	out, err := review(t, stub, map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/70"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The approval is reported back as an opinion...
	opinions := reviewsOf(t, out)
	if len(opinions) != 1 || opinions[0].Author != "alice" || opinions[0].State != "approved" || opinions[0].Body != "ship it" {
		t.Fatalf("opinions=%+v want alice/approved/ship it", opinions)
	}
	if got := out["review_summary"]; got != "1 approved" {
		t.Errorf("review_summary=%q want %q", got, "1 approved")
	}

	// ...but it is not acted on: the capability never merges.
	if out["merged"] != "false" || out["requires_human_approval"] != "true" {
		t.Fatalf("out=%v want merged=false and requires_human_approval=true", out)
	}

	// Nor does it ask GitHub to act: an approval must not trigger any write.
	for _, req := range stub.requests {
		if req.method != http.MethodGet {
			t.Fatalf("the capability made a %s call (%s); approval must not trigger a write", req.method, req.path)
		}
		if strings.HasSuffix(req.path, "/merge") {
			t.Fatalf("the capability called the merge endpoint on an approved pull request: %s %s", req.method, req.path)
		}
	}
}

// TestPullRequestReviewReadsThePullRequestTheURLNames: a reference the caller
// named outright is read by number — no branch-pair lookup — and its reviews come
// back under the number it named.
func TestPullRequestReviewReadsThePullRequestTheURLNames(t *testing.T) {
	stub := newGitHubStub()
	// A list lookup would find a different pull request, so prove there is none.
	stub.pullsBody = `[]`
	out, err := review(t, stub, map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/70"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lists := stub.calls(http.MethodGet, "/pulls"); len(lists) != 0 {
		t.Fatalf("branch-pair list calls=%+v, want none when the pull request is named", lists)
	}
	reads := stub.calls(http.MethodGet, "/pulls/70")
	if len(reads) != 1 {
		t.Fatalf("read calls=%+v want one at the one-pull-request endpoint", stub.requests)
	}
	reviews := stub.calls(http.MethodGet, "/reviews")
	if len(reviews) != 1 || reviews[0].path != "/repos/kaulie/autonomy/pulls/70/reviews" {
		t.Fatalf("review calls=%+v want one at /repos/kaulie/autonomy/pulls/70/reviews", reviews)
	}
	if got := out["number"]; got != "70" {
		t.Errorf("number=%q want 70", got)
	}
}

// TestPullRequestReviewSummarizesReviewStates: the tally names each state it saw,
// and a pull request with no reviews says so rather than returning an empty
// string.
func TestPullRequestReviewSummarizesReviewStates(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		count   string
		summary string
	}{
		{"none", `[]`, "0", "no reviews yet"},
		{"one approval", `[{"id":1,"user":{"login":"a"},"state":"APPROVED","body":""}]`, "1", "1 approved"},
		{"an unknown state reads as a comment", `[{"id":1,"user":{"login":"a"},"state":"SOMETHING_NEW","body":"hm"}]`, "1", "1 commented"},
		{"dismissed", `[{"id":1,"user":{"login":"a"},"state":"DISMISSED","body":""}]`, "1", "1 dismissed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub()
			stub.reviewsBody = tc.body
			out, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := out["reviews_count"]; got != tc.count {
				t.Errorf("reviews_count=%q want %q", got, tc.count)
			}
			if got := out["review_summary"]; got != tc.summary {
				t.Errorf("review_summary=%q want %q", got, tc.summary)
			}
		})
	}
}

// TestPullRequestReviewReadsThePullReferencesItAccepts: every shape of pull
// request reference the description promises is read as the same pull request,
// and its reviews are read with it.
func TestPullRequestReviewReadsThePullReferencesItAccepts(t *testing.T) {
	forms := []string{
		"https://github.com/kaulie/autonomy/pull/70",
		"https://github.com/kaulie/autonomy/pulls/70",
		"https://github.com/kaulie/autonomy/pull/70/",
		"https://github.com/kaulie/autonomy/pull/70#discussion_r1",
		"kaulie/autonomy#70",
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			stub := newGitHubStub()
			out, err := review(t, stub, map[string]string{"pr": form})
			if err != nil {
				t.Fatalf("Run with %q: %v", form, err)
			}
			if reads := stub.calls(http.MethodGet, "/pulls/70"); len(reads) != 1 {
				t.Fatalf("read calls=%+v want one at /pulls/70 for %q", stub.requests, form)
			}
			if got := out["number"]; got != "70" {
				t.Errorf("number=%q want 70", got)
			}
		})
	}
	t.Run("bare number with repo", func(t *testing.T) {
		stub := newGitHubStub()
		out, err := review(t, stub, map[string]string{"pr": "70"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := out["number"]; got != "70" {
			t.Errorf("number=%q want 70", got)
		}
	})
}

// TestPullRequestReviewTakesThePullRequestUnderItsAliases: pr / pr_url /
// pull_request all name the same input.
func TestPullRequestReviewTakesThePullRequestUnderItsAliases(t *testing.T) {
	for _, key := range []string{"pr", "pr_url", "pull_request"} {
		t.Run(key, func(t *testing.T) {
			stub := newGitHubStub()
			out, err := review(t, stub, map[string]string{key: "https://github.com/kaulie/autonomy/pull/70"})
			if err != nil {
				t.Fatalf("Run with %q: %v", key, err)
			}
			if got := out["number"]; got != "70" {
				t.Errorf("number=%q want 70", got)
			}
		})
	}
}

// TestPullRequestReviewRefusesAnUnreadablePullReference: a reference it cannot
// read is a failure before any call — not something to guess at.
func TestPullRequestReviewRefusesAnUnreadablePullReference(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/kaulie/autonomy",        // a repository, not a pull request
		"kaulie/autonomy",                           // nor this
		"kaulie/autonomy#abc",                       // a fragment that is not a number
		"github.com/kaulie/autonomy/pull/70",        // a host without a scheme cannot be read as owner/name
		"https://github.com/kaulie/autonomy/pull/",  // no number
		"https://github.com/kaulie/autonomy/pull/0", // pull requests are numbered from one
		"https://github.com/kaulie/autonomy/pull/abc",
	} {
		t.Run(ref, func(t *testing.T) {
			stub := newGitHubStub()
			_, err := review(t, stub, map[string]string{"pr": ref})
			if err == nil || !strings.Contains(err.Error(), "cannot read a pull request") {
				t.Fatalf("err=%v want an unreadable pull request reference", err)
			}
			if len(stub.requests) != 0 {
				t.Errorf("called GitHub while reading a reference it cannot use: %+v", stub.requests)
			}
		})
	}
}

// TestPullRequestReviewRefusesWhenTheReferenceAndTheInputsDisagree: a call that
// names two different things is a mistake, not a choice to be made silently.
func TestPullRequestReviewRefusesWhenTheReferenceAndTheInputsDisagree(t *testing.T) {
	const ref = "https://github.com/kaulie/autonomy/pull/70"
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"another repository", map[string]string{"pr": ref, "repo": "kaulie/other"}, `the pull request url names kaulie/autonomy but "repo" says kaulie/other`},
		{"another head branch", map[string]string{"pr": ref, "from": "feature/y"}, "is from feature/x, not from feature/y"},
		{"another base branch", map[string]string{"pr": ref, "to": "release"}, "targets main, not release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub()
			_, err := review(t, stub, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want it to mention %q", err, tc.want)
			}
			if reviews := stub.calls(http.MethodGet, "/reviews"); len(reviews) != 0 {
				t.Errorf("read reviews despite a disagreement: %+v", reviews)
			}
		})
	}
}

// TestPullRequestReviewAgreesWithThePullRequestItNamed: passing the branches the
// pull request really has is not a disagreement — the repository matches whatever
// its case — and the reviews are still read.
func TestPullRequestReviewAgreesWithThePullRequestItNamed(t *testing.T) {
	stub := newGitHubStub()
	out, err := review(t, stub, map[string]string{
		"pr":   "https://github.com/kaulie/autonomy/pull/70",
		"repo": "KAULIE/autonomy",
		"from": "feature/x",
		"to":   "main",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["merged"] != "false" {
		t.Errorf("merged=%q want false", out["merged"])
	}
	if reviews := stub.calls(http.MethodGet, "/reviews"); len(reviews) != 1 {
		t.Fatalf("review calls=%+v want one", reviews)
	}
}

// TestPullRequestReviewReportsAPullRequestThatIsNotThere: a pull request that is
// gone (already merged, or never there) comes back as not_found — the planner's
// cue that there is nothing to read — instead of as an HTTP status.
func TestPullRequestReviewReportsAPullRequestThatIsNotThere(t *testing.T) {
	stub := newGitHubStub()
	stub.pullStatus = http.StatusNotFound
	stub.pullBody = `{"message":"Not Found"}`
	_, err := review(t, stub, map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/70"})
	if err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("err=%v want not_found", err)
	}
}

// TestPullRequestReviewReportsTheGitHostsRefusalOnReviews: the git host's own
// reason for refusing to hand over the reviews is passed through, not swallowed.
func TestPullRequestReviewReportsTheGitHostsRefusalOnReviews(t *testing.T) {
	stub := newGitHubStub()
	stub.reviewsStatus = http.StatusForbidden
	stub.reviewsBody = `{"message":"API rate limit exceeded"}`
	_, err := review(t, stub, map[string]string{"from": "feature/x", "to": "main"})
	if err == nil {
		t.Fatal("Run succeeded; want GitHub's refusal")
	}
	for _, want := range []string{"HTTP 403", "rate limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
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

// TestPullRequestReviewReadsTheRepositoryOutOfTheGitRemote: a task workspace
// knows its project as a git remote, and that is enough to name the repository —
// the reviews are then read from it.
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
			out, err := c.Run(map[string]string{"from": "feature/x", "to": "main"})
			if err != nil {
				t.Fatalf("Run with %s: %v", remote, err)
			}
			if out["merged"] != "false" {
				t.Errorf("merged=%q want false", out["merged"])
			}
			reads := stub.calls(http.MethodGet, "/reviews")
			if len(reads) != 1 || reads[0].path != "/repos/kaulie/autonomy/pulls/70/reviews" {
				t.Fatalf("review calls=%+v want one at /repos/kaulie/autonomy/pulls/70/reviews", reads)
			}
		})
	}
}

// TestPullRequestReviewNeedsWhatItCannotGuess covers the inputs it refuses to
// invent: the head branch, the repository and the credential. All of these fail
// before a single call is made.
func TestPullRequestReviewNeedsWhatItCannotGuess(t *testing.T) {
	cases := []struct {
		name  string
		in    map[string]string
		repo  string
		token string
		want  string
	}{
		{"no from", map[string]string{"to": "main"}, "kaulie/autonomy", "t", "missing from"},
		{"no from and no pull request", map[string]string{"to": "main"}, "kaulie/autonomy", "t", "no pull request was named"},
		{"no repo", map[string]string{"from": "feature/x"}, "", "t", "missing repository"},
		{"no repo for a bare number", map[string]string{"pr": "70"}, "", "t", "missing repository"},
		{"no credential", map[string]string{"from": "feature/x"}, "kaulie/autonomy", "", "no credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", "")
			t.Setenv("GIT_REPO_URL", "")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("PR_BASE_BRANCH", "")
			_, err := (sd.PullRequestReview{APIURL: "http://127.0.0.1:1", Token: tc.token, Repo: tc.repo, GhAuthToken: noGitHubCLI}).Run(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want it to mention %q", err, tc.want)
			}
		})
	}
}
