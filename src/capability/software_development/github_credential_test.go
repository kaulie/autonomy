package software_development_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// TestPullRequestReviewCredentialSources pins where the credential comes from and
// in what order: the token it was given, GITHUB_TOKEN, GH_TOKEN, and last the gh
// CLI — the credential the machine and web-cursor already use to open and land
// pull requests.
func TestPullRequestReviewCredentialSources(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		githubEnv string
		ghEnv     string
		fromCLI   string
		want      string
	}{
		{"the token it was given", "field-token", "env-token", "alt-token", "cli-token", "field-token"},
		{"GITHUB_TOKEN", "", "env-token", "alt-token", "cli-token", "env-token"},
		{"GH_TOKEN", "", "", "alt-token", "cli-token", "alt-token"},
		{"the gh CLI", "", "", "", "cli-token", "cli-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub()
			srv := stub.start(t)
			t.Setenv("GITHUB_REPOSITORY", "kaulie/autonomy")
			t.Setenv("GIT_REPO_URL", "")
			t.Setenv("GITHUB_TOKEN", tc.githubEnv)
			t.Setenv("GH_TOKEN", tc.ghEnv)

			c := sd.PullRequestReview{
				APIURL: srv.URL, HTTPClient: srv.Client(), Token: tc.token,
				GhAuthToken: func(context.Context) (string, error) { return tc.fromCLI, nil },
			}
			if _, err := c.Run(map[string]string{"from": "feature/x", "to": "main"}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			merges := stub.calls(http.MethodPut, "/merge")
			if len(merges) != 1 {
				t.Fatalf("merge calls=%d, want 1", len(merges))
			}
			if got, want := merges[0].auth, "Bearer "+tc.want; got != want {
				t.Fatalf("Authorization=%q, want %q", got, want)
			}
		})
	}
}

// TestGitHubCredentialFallsBackToTheGhCLI: the default source is the gh CLI
// itself, so a runtime started without GITHUB_TOKEN in its environment is not a
// dead end on a machine that is already signed in. A fake gh on PATH is what makes
// the real code path testable anywhere.
func TestGitHubCredentialFallsBackToTheGhCLI(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1 $2\" = \"auth token\" ]; then echo gho_from_cli; else exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_REPOSITORY", "kaulie/autonomy")
	t.Setenv("GIT_REPO_URL", "")

	stub := newGitHubStub()
	srv := stub.start(t)
	// No Token and no environment token: this is the run that used to end in
	// "no credential".
	if _, err := (sd.PullRequestReview{APIURL: srv.URL, HTTPClient: srv.Client()}).Run(map[string]string{"from": "feature/x", "to": "main"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	merges := stub.calls(http.MethodPut, "/merge")
	if len(merges) != 1 || merges[0].auth != "Bearer gho_from_cli" {
		t.Fatalf("merge calls=%+v, want one authorized with the gh CLI's token", merges)
	}
}

// TestGitHubCredentialRefusalNamesEverySource: "no credential" without saying
// where one would be looked for is a dead end of its own.
func TestGitHubCredentialRefusalNamesEverySource(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_REPOSITORY", "kaulie/autonomy")
	t.Setenv("GIT_REPO_URL", "")

	_, err := (sd.PullRequestReview{
		APIURL: "http://127.0.0.1:1", Repo: "kaulie/autonomy",
		GhAuthToken: noGitHubCLI,
	}).Run(map[string]string{"from": "feature/x"})
	if err == nil {
		t.Fatal("expected the refusal")
	}
	for _, want := range []string{"no credential", sd.EnvGitHubToken, sd.EnvGitHubTokenAlt, "gh auth login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
