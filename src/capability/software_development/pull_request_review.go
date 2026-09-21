package software_development

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// ReviewName is the capability's stable semantic name: read a pull request's
	// review opinions (it does not merge).
	ReviewName = "pull_request.review"
	// ReviewDomain is the semantic domain it belongs to — landing code is part
	// of software development, the domain code_edit and service.deploy live in.
	ReviewDomain = "software_development"
	// ReviewProvider is who really does it: the git host (GitHub), not autonomy.
	// Like service.deploy naming the deployment control plane, the provider says
	// where the effect happens.
	ReviewProvider = "github"

	// DefaultGitHubAPIURL is where GitHub's REST API lives. GITHUB_API_URL
	// overrides it (GitHub Enterprise, or a test server), so pointing the
	// capability somewhere else needs no code change.
	DefaultGitHubAPIURL = "https://api.github.com"

	// EnvGitHubToken / EnvGitHubTokenAlt carry the credential the reads are made
	// with. GH_TOKEN is the gh CLI's own variable, accepted so a host that
	// already exports it needs no extra setting.
	EnvGitHubToken    = "GITHUB_TOKEN"
	EnvGitHubTokenAlt = "GH_TOKEN"
	// EnvGitHubAPIURL overrides the API base URL.
	EnvGitHubAPIURL = "GITHUB_API_URL"
	// EnvRepository names the repository (owner/name) when the caller does not.
	EnvRepository = "GITHUB_REPOSITORY"
	// EnvGitRepoURL is the project's git remote — the URL a task workspace was
	// cloned from; the repository is read out of it.
	EnvGitRepoURL = "GIT_REPO_URL"
	// EnvBaseBranch overrides which branch counts as the trunk.
	EnvBaseBranch = "PR_BASE_BRANCH"

	// The input keys that name the pull request itself rather than its branches:
	// the URL everyone already has from the pull request (a code_edit report, a
	// previous action's output) identifies it outright, so no branch pair and no
	// repository lookup are needed.
	inputPull     = "pr"
	inputPullURL  = "pr_url"
	inputPullAlt  = "pull_request"
	inputFrom     = "from"
	inputFromAlt  = "from_branch"
	inputFromHead = "head"
	inputFromSrc  = "source"
	inputTo       = "to"
	inputToAlt    = "to_branch"
	inputToBase   = "base"
	inputToTarget = "target"

	// DefaultBaseBranch is the last resort for the trunk, when neither the input
	// nor PR_BASE_BRANCH nor the repository itself says what it is.
	DefaultBaseBranch = "main"

	// The review states GitHub reports on a review opinion. They are named
	// lower-case in the output so a caller reads them the same way it reads the
	// rest of the answer.
	reviewStateApproved         = "approved"
	reviewStateChangesRequested = "changes_requested"
	reviewStateCommented        = "commented"
	reviewStateDismissed        = "dismissed"
	reviewStatePending          = "pending"

	// reviewHTTPTimeout bounds one GitHub call. Reading a pull request and its
	// reviews is a couple of small requests; anything slower than this is a
	// network problem, not patience.
	reviewHTTPTimeout = 30 * time.Second
	// reviewMaxBody bounds how much of a GitHub response is read into memory.
	reviewMaxBody = 1 << 20
	// reviewAPIVersion pins the REST API version these payloads are written
	// against.
	reviewAPIVersion = "2022-11-28"
	// reviewSnippetChars bounds a non-JSON error body kept in a message.
	reviewSnippetChars = 200
)

// PullRequestReview reads the review opinions on one pull request and reports
// them back, identified either by its own reference (`pr`: its URL, or
// owner/name#number) or by the branch pair it was opened from (`from` → `to`).
//
// It deliberately does NOT merge. Landing a change is a decision a human has to
// make and own, so the capability stops at the observation and says so plainly:
// the answer carries `merged:"false"` and `requires_human_approval:"true"` next
// to the pull request's reviews, and it never touches GitHub's merge endpoint —
// there is no merge path to trigger, not an optional one that is turned off.
//
// It is deliberately not an agent. Reading a pull request and its reviews is a
// deterministic call against the git host with a verifiable answer, so it is
// code over the GitHub REST API and acquires no worker.
type PullRequestReview struct {
	// APIURL overrides GITHUB_API_URL / the public API (tests, or an explicit
	// per-call host).
	APIURL string
	// Token overrides GITHUB_TOKEN / GH_TOKEN.
	Token string
	// GhAuthToken reads the credential when neither Token nor the environment has
	// one (nil asks the gh CLI — see github_credential.go). Tests inject it so a
	// run never depends on whose gh happens to be signed in.
	GhAuthToken GhAuthTokenFn
	// Repo is the repository (owner/name) when the input does not name one.
	Repo string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

func (PullRequestReview) Name() string { return ReviewName }

func (PullRequestReview) Domain() string { return ReviewDomain }

func (PullRequestReview) Provider() string { return ReviewProvider }

func (PullRequestReview) Description() string {
	return `read one pull request's review opinions — it does NOT merge. Name the pull request either outright — "pr":"https://<host>/owner/name/pull/43" ("pr_url"/"pull_request" work too; "owner/name#43" and a bare "43" with "repo" are accepted) — or by its branches: {"from":"<head/topic branch>","to":"<base branch>"} ("from_branch"/"head" and "to_branch"/"base" work too; an empty "to" means the trunk: PR_BASE_BRANCH, else the repository's default branch, else main). A "repo" that contradicts the pull request url is refused rather than guessed at. The answer is the pull request's reviews plus merged:"false" and requires_human_approval:"true": the capability never merges, so a human has to approve and perform the merge. output: {"from","to","number","pr","title","state","draft","merged":"false","requires_human_approval":"true","reviews","reviews_count","review_summary"} — reviews is the review opinions (author / state / body) as JSON`
}

// Inputs / Outputs declare the capability's call signature for {{CONSTRUCTS}}.
func (PullRequestReview) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "pr", Aliases: []string{"pr_url", "pull_request"}, Description: "the pull request to read, named outright — the url everyone already has from it: https://<host>/owner/name/pull/<number>, owner/name#<number>, or <number> when repo names the repository. Required unless the pull request is named by from/to instead; nothing is read from the branches when it is given"},
		{Name: "from", Aliases: []string{"from_branch", "head", "source"}, Description: "the head/topic branch whose pull request to read — required unless pr names the pull request; when both are given they must agree"},
		{Name: "to", Aliases: []string{"to_branch", "base", "target"}, Description: "the base branch; empty means the trunk: PR_BASE_BRANCH, else the repository's default branch, else main. With pr it must match the pull request's own base"},
		{Name: "repo", Aliases: []string{"repository"}, Description: "owner/name, when neither pr nor GITHUB_REPOSITORY / GIT_REPO_URL names the repository; it must not contradict pr"},
	}
}

func (PullRequestReview) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "from", Description: "the head branch the pull request came from"},
		{Name: "to", Description: "the base branch the pull request targets"},
		{Name: "number", Description: "the pull request number"},
		{Name: "pr", Description: "the pull request's url"},
		{Name: "title", Description: "the pull request's title"},
		{Name: "state", Description: `the pull request's state: "open" / "closed"`},
		{Name: "draft", Description: `"true" when the pull request is a draft`},
		{Name: "merged", Description: `always "false": this capability never merges`},
		{Name: "requires_human_approval", Description: `always "true": a human must approve and perform the merge`},
		{Name: "reviews", Description: "the pull request's review opinions as a JSON array of {author, state, body, submitted_at, url}"},
		{Name: "reviews_count", Description: "how many review opinions the pull request has"},
		{Name: "review_summary", Description: `a one-line tally of the review states, e.g. "1 approved, 1 changes_requested"`},
	}
}

// Run resolves which pull request this call is about, reads its review opinions
// and reports them back. It never merges: landing the change is left to a human,
// which the answer says outright (merged:"false", requires_human_approval:"true").
// Every refusal carries its own reason, so it reaches the planner as evidence
// rather than as a generic error.
func (c PullRequestReview) Run(in map[string]string) (map[string]string, error) {
	// A pull request can be named two ways: by its own reference — the URL
	// everyone already has from it (a code_edit report, a previous action's
	// output) — or by the branch pair it was opened from.
	named, err := parsePullReference(firstNonEmpty(in[inputPull], in[inputPullURL], in[inputPullAlt]))
	if err != nil {
		return nil, err
	}
	from := strings.TrimSpace(firstNonEmpty(in[inputFrom], in[inputFromAlt], in[inputFromHead], in[inputFromSrc]))
	if named.number == 0 && from == "" {
		return nil, fmt.Errorf("%s: missing from (the head branch whose pull request should be read) and no pull request was named (pass \"pr\":\"<pull request url>\")", ReviewName)
	}
	// What the caller names outright wins over the environment's default
	// repository, and a "repo" that contradicts it is a mistake rather than a
	// choice to be made here: reading a different repository than the one the
	// caller named is exactly the silent outcome this capability must not have.
	repo := normalizeRepo(firstNonEmpty(in["repo"], in["repository"], c.Repo))
	if named.repo != "" {
		if repo != "" && !strings.EqualFold(repo, named.repo) {
			return nil, fmt.Errorf("%s: the pull request url names %s but \"repo\" says %s", ReviewName, named.repo, repo)
		}
		repo = named.repo
	}
	if repo == "" {
		repo = normalizeRepo(firstNonEmpty(os.Getenv(EnvRepository), os.Getenv(EnvGitRepoURL)))
	}
	if repo == "" {
		return nil, fmt.Errorf("%s: missing repository (pass \"repo\":\"owner/name\", or set %s / %s)", ReviewName, EnvRepository, EnvGitRepoURL)
	}
	token, _ := c.credential(c.Token)
	if token == "" {
		return nil, noCredential()
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: reviewHTTPTimeout}
	}
	apiURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.APIURL, os.Getenv(EnvGitHubAPIURL), DefaultGitHubAPIURL)), "/")

	// Which pull request this call is about is settled before any read, so the
	// reviews fetched below are about one concrete pull request with a base
	// branch.
	requested := firstNonEmpty(in[inputTo], in[inputToAlt], in[inputToBase], in[inputToTarget])
	pr, _, err := c.identifyPull(client, apiURL, repo, token, named, from, requested)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}

	reviews, err := c.listReviews(client, apiURL, repo, token, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}
	opinions, err := json.Marshal(reviewOpinions(reviews))
	if err != nil {
		return nil, fmt.Errorf("%s: encode reviews: %w", ReviewName, err)
	}

	// The answer is the observation, never a merge. merged is always false and
	// requires_human_approval always true: this capability has no merge path, by
	// design, so a caller can never read a merge out of it — a human has to
	// approve and perform that.
	return map[string]string{
		"from":                    pr.Head.Ref,
		"to":                      pr.Base.Ref,
		"number":                  strconv.Itoa(pr.Number),
		"pr":                      pr.HTMLURL,
		"title":                   pr.Title,
		"state":                   pr.State,
		"draft":                   strconv.FormatBool(pr.Draft),
		"merged":                  "false",
		"requires_human_approval": "true",
		"reviews":                 string(opinions),
		"reviews_count":           strconv.Itoa(len(reviews)),
		"review_summary":          reviewSummary(reviews),
	}, nil
}

// identifyPull settles which pull request this call is about, and which branch it
// targets.
//
// A reference the caller named (its URL, its number) needs no list lookup: the
// pull request is read by number, and the branch pair the caller may have passed
// alongside has to agree with what the pull request actually is — a call that
// names two different things is a mistake, and reading one of them silently is
// exactly what this capability must not do. Without a reference, the base
// branch is resolved first and the open pull request from `from` into it is the
// one meant.
func (c PullRequestReview) identifyPull(client *http.Client, apiURL, repo, token string, named pullReference, from, requested string) (gitHubPull, string, error) {
	if named.number > 0 {
		pr, err := c.getPullRequest(client, apiURL, repo, token, named.number)
		if err != nil {
			return gitHubPull{}, "", err
		}
		if from != "" && from != pr.Head.Ref {
			return gitHubPull{}, "", fmt.Errorf("pull request #%d (%s) is from %s, not from %s", pr.Number, pr.HTMLURL, pr.Head.Ref, from)
		}
		if wanted := strings.TrimSpace(requested); wanted != "" && wanted != pr.Base.Ref {
			return gitHubPull{}, "", fmt.Errorf("pull request #%d (%s) targets %s, not %s", pr.Number, pr.HTMLURL, pr.Base.Ref, wanted)
		}
		return pr, pr.Base.Ref, nil
	}
	// The base branch is resolved first: everything below (which pull request,
	// which gates) is about the from → to pair, not about a branch name alone.
	to, err := c.resolveBase(client, apiURL, repo, token, requested)
	if err != nil {
		return gitHubPull{}, "", err
	}
	pr, err := c.findPullRequest(client, apiURL, repo, token, from, to)
	if err != nil {
		return gitHubPull{}, "", err
	}
	return pr, to, nil
}

// resolveBase decides which branch the pull request must target: what the caller
// asked for, then PR_BASE_BRANCH, then the repository's own default branch, then
// main. A repository that cannot be read is not an error here — the pull request
// lookup below reports that far more precisely — so the fallback stands.
func (c PullRequestReview) resolveBase(client *http.Client, apiURL, repo, token, requested string) (string, error) {
	if to := strings.TrimSpace(requested); to != "" {
		return to, nil
	}
	if to := strings.TrimSpace(os.Getenv(EnvBaseBranch)); to != "" {
		return to, nil
	}
	status, body, err := c.get(client, fmt.Sprintf("%s/repos/%s", apiURL, repo), token)
	if err == nil && status >= 200 && status <= 299 {
		var r gitHubRepo
		if json.Unmarshal(body, &r) == nil {
			if def := strings.TrimSpace(r.DefaultBranch); def != "" {
				return def, nil
			}
		}
	}
	return DefaultBaseBranch, nil
}

// findPullRequest looks the pull request up by its branches: the open one whose
// head is `from` and whose base is `to`. GitHub allows at most one such pair, so
// an empty result is a real "there is no such pull request" and not a paging
// artefact.
func (c PullRequestReview) findPullRequest(client *http.Client, apiURL, repo, token, from, to string) (gitHubPull, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", headParam(repo, from))
	q.Set("base", to)
	q.Set("sort", "created")
	q.Set("direction", "desc")
	q.Set("per_page", "10")
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?%s", apiURL, repo, q.Encode())

	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return gitHubPull{}, err
	}
	if err := gitHubExpect(status, body, fmt.Sprintf("list pull requests %s → %s", from, to)); err != nil {
		return gitHubPull{}, err
	}
	var pulls []gitHubPull
	if err := json.Unmarshal(body, &pulls); err != nil {
		return gitHubPull{}, fmt.Errorf("decode pull request list: %w", err)
	}
	if len(pulls) == 0 {
		return gitHubPull{}, fmt.Errorf("not_found: %s has no open pull request from %s into %s", repo, from, to)
	}
	return pulls[0], nil
}

// getPullRequest reads one pull request by number — the lookup a caller that
// named the pull request outright asked for. It reports the same not_found reason
// the branch-pair lookup reports, so the planner can act on either: a pull
// request that is already merged (or closed) is not there anymore, and saying so
// is the answer, not a merge.
func (c PullRequestReview) getPullRequest(client *http.Client, apiURL, repo, token string, number int) (gitHubPull, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", apiURL, repo, number)
	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return gitHubPull{}, err
	}
	if status == http.StatusNotFound {
		return gitHubPull{}, fmt.Errorf("not_found: %s has no pull request #%d", repo, number)
	}
	if err := gitHubExpect(status, body, fmt.Sprintf("read pull request %s#%d", repo, number)); err != nil {
		return gitHubPull{}, err
	}
	var pr gitHubPull
	if err := json.Unmarshal(body, &pr); err != nil {
		return gitHubPull{}, fmt.Errorf("decode pull request: %w", err)
	}
	return pr, nil
}

// listReviews reads the pull request's review opinions — the reviews people left
// on it (approved / changes_requested / commented / …). This is the observation
// the capability reports back in place of a merge.
func (c PullRequestReview) listReviews(client *http.Client, apiURL, repo, token string, number int) ([]gitHubReview, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews?per_page=100", apiURL, repo, number)
	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return nil, err
	}
	if err := gitHubExpect(status, body, fmt.Sprintf("list reviews for %s#%d", repo, number)); err != nil {
		return nil, err
	}
	var reviews []gitHubReview
	if err := json.Unmarshal(body, &reviews); err != nil {
		return nil, fmt.Errorf("decode reviews: %w", err)
	}
	return reviews, nil
}

func (c PullRequestReview) get(client *http.Client, endpoint, token string) (int, []byte, error) {
	return c.request(client, endpoint, token)
}

// request is the one place a GitHub call happens, and it is deliberately
// read-only: it always issues a GET, so the capability cannot approve or merge a
// pull request even by accident — there is no method and no request body that
// could turn this into a write. The version-pinned headers (including the
// credential) are set here and the response body is read bounded, so no caller
// can pull an unbounded response into memory.
func (c PullRequestReview) request(client *http.Client, endpoint, token string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", reviewAPIVersion)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, reviewMaxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, payload, nil
}

// gitHubExpect turns a non-2xx into an error, so every caller can share the same
// "call, then judge" shape.
func gitHubExpect(status int, body []byte, what string) error {
	if status >= 200 && status <= 299 {
		return nil
	}
	return gitHubError(status, body, what)
}

// gitHubError carries the git host's own reason — "Not Found", "Pull Request is
// not mergeable" — instead of a bare status code, so the planner can act on it.
func gitHubError(status int, body []byte, what string) error {
	var e struct {
		Message string `json:"message"`
	}
	msg := ""
	if err := json.Unmarshal(body, &e); err == nil {
		msg = strings.TrimSpace(e.Message)
	}
	if msg == "" {
		msg = bodySnippet(body)
	}
	return fmt.Errorf("%s: HTTP %d: %s", what, status, msg)
}

// bodySnippet is a bounded, single-line rendering of a response body.
func bodySnippet(body []byte) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(string(body))), " ")
	if text == "" {
		return "no response body"
	}
	if len(text) > reviewSnippetChars {
		text = text[:reviewSnippetChars] + "…"
	}
	return text
}

// pullReference is the pull request a caller named outright rather than by its
// branches: the repository it lives in (empty when the caller named only a
// number) and the number itself.
type pullReference struct {
	repo   string
	number int
}

// parsePullReference reads the pull request a caller passed as its own reference
// — what everyone already has from it (a code_edit report, a previous action's
// output):
//
//	https://github.com/owner/name/pull/43   (any host; a trailing /, a query or
//	                                         a #discussion fragment is cut away,
//	                                         and /pulls/ is accepted too)
//	owner/name#43
//	43                                      (or #43, with "repo" naming the repo)
//
// An empty reference is not a pull request — the caller is naming it by its
// branches — and yields the zero value. A reference that is there but unreadable
// is an error rather than a guess: landing a repository or a pull request nobody
// named is the one outcome this capability must not have.
func parsePullReference(raw string) (pullReference, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return pullReference{}, nil
	}
	unreadable := func() (pullReference, error) {
		return pullReference{}, fmt.Errorf("%s: cannot read a pull request from %q (want https://<host>/owner/name/pull/<number>, owner/name#<number>, or <number> with \"repo\")", ReviewName, s)
	}

	// The URL form: everything before /pull is the repository, everything after
	// it is the number (plus whatever a browser URL drags along).
	if i := strings.Index(strings.ToLower(s), "/pull"); i >= 0 {
		repo := repoFromPrefix(s[:i])
		tail := strings.TrimLeft(strings.TrimPrefix(s[i+len("/pull"):], "s"), "/")
		if j := strings.IndexAny(tail, "?#/"); j >= 0 {
			tail = tail[:j]
		}
		number, ok := pullNumber(tail)
		if !ok || repo == "" {
			return unreadable()
		}
		return pullReference{repo: repo, number: number}, nil
	}
	// The number-only form: the repository then has to come from elsewhere
	// ("repo" / GITHUB_REPOSITORY / GIT_REPO_URL, checked by Run).
	if number, ok := pullNumber(strings.TrimPrefix(s, "#")); ok && !strings.Contains(s, "/") {
		return pullReference{number: number}, nil
	}
	// owner/name#43 — and a URL with the number in a fragment, which the same
	// shape covers.
	if j := strings.LastIndexByte(s, '#'); j > 0 {
		number, ok := pullNumber(s[j+1:])
		if repo := repoFromPrefix(s[:j]); ok && repo != "" {
			return pullReference{repo: repo, number: number}, nil
		}
	}
	return unreadable()
}

// repoFromPrefix reads the repository out of the part of a reference that comes
// before the pull request number: an https or ssh remote, or a plain owner/name.
// A scheme-less host (github.com/owner/name) is not read as owner/name — that
// would name a repository nobody named — so it yields nothing and the reference
// is refused instead of guessed at.
func repoFromPrefix(prefix string) string {
	if strings.Contains(prefix, "://") || strings.Contains(prefix, "@") {
		return normalizeRepo(prefix)
	}
	parts := strings.Split(strings.Trim(prefix, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// pullNumber reads a pull request number: digits only, and never zero.
func pullNumber(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// normalizeRepo accepts what a caller reasonably has — "owner/name", an https
// remote, or an ssh remote — and returns owner/name. Anything it cannot read as
// a repository is empty, which the caller reports as a missing repository rather
// than guessing one.
func normalizeRepo(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		// scheme://[user@]host[:port]/owner/name
		s = s[i+3:]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[j+1:]
		} else {
			return ""
		}
		return repoSegments(s)
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		// scp-like: git@host:owner/name (or git@host/owner/name)
		s = strings.TrimPrefix(s[i+1:], "/")
		if j := strings.IndexAny(s, ":/"); j >= 0 {
			s = s[j+1:]
		}
	}
	return repoSegments(s)
}

// repoSegments keeps owner/name and drops anything a git URL carries around it.
func repoSegments(s string) string {
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// headParam is how the pull request list filters by branch: a same-repository
// branch is addressed as owner:branch, and a caller that already passed
// owner:branch is left alone.
func headParam(repo, head string) string {
	if strings.Contains(head, ":") {
		return head
	}
	owner := repo
	if i := strings.IndexByte(repo, '/'); i > 0 {
		owner = repo[:i]
	}
	return owner + ":" + head
}

// gitHubPull is the slice of a pull request this capability observes.
type gitHubPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// gitHubRepo is the slice of a repository used to learn the trunk.
type gitHubRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// gitHubReview is one review on a pull request, as GitHub reports it.
type gitHubReview struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string `json:"state"`
	Body        string `json:"body"`
	SubmittedAt string `json:"submitted_at"`
	HTMLURL     string `json:"html_url"`
}

// reviewOpinion is the slice of a review this capability hands back: who left it,
// what they concluded, and what they wrote.
type reviewOpinion struct {
	Author    string `json:"author"`
	State     string `json:"state"`
	Body      string `json:"body"`
	Submitted string `json:"submitted_at,omitempty"`
	URL       string `json:"url,omitempty"`
}

// reviewOpinions renders the reviews the caller reads back.
func reviewOpinions(reviews []gitHubReview) []reviewOpinion {
	out := make([]reviewOpinion, 0, len(reviews))
	for _, r := range reviews {
		out = append(out, reviewOpinion{
			Author:    r.User.Login,
			State:     reviewState(r.State),
			Body:      strings.TrimSpace(r.Body),
			Submitted: r.SubmittedAt,
			URL:       r.HTMLURL,
		})
	}
	return out
}

// reviewState lower-cases GitHub's review state. An unrecognised one (or an
// empty one) reads as a comment rather than being dropped: the review is still
// an opinion someone left.
func reviewState(state string) string {
	s := strings.ToLower(strings.TrimSpace(state))
	switch s {
	case reviewStateApproved, reviewStateChangesRequested, reviewStateCommented, reviewStateDismissed, reviewStatePending:
		return s
	default:
		return reviewStateCommented
	}
}

// reviewSummary is a one-line tally of the review states, so a caller reads the
// gist without parsing the JSON.
func reviewSummary(reviews []gitHubReview) string {
	if len(reviews) == 0 {
		return "no reviews yet"
	}
	counts := map[string]int{}
	var order []string
	for _, r := range reviews {
		state := reviewState(r.State)
		if counts[state] == 0 {
			order = append(order, state)
		}
		counts[state]++
	}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
	}
	return strings.Join(parts, ", ")
}
