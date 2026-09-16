package software_development

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Where the credential for a GitHub call comes from, in order: the caller's own
// value (the capability field, then the input), GITHUB_TOKEN, GH_TOKEN, and — last
// — the gh CLI's stored credential.
//
// The last source is deliberate. This follows web-cursor, which does all of its
// GitHub work by running `gh`: on a machine where the agents open their pull
// requests that way, `gh auth login` *is* the GitHub credential, and a runtime
// started without GITHUB_TOKEN in its environment should not be a dead end. It is
// consulted only when nothing else answered, so an explicit token always wins and a
// host without the CLI behaves exactly as before.
//
// Sources are named in the refusal, because "no credential" without saying where
// one would be looked for is a dead end of its own.

// ghAuthTimeout bounds the `gh auth token` call: reading a local credential is
// fast, and anything slower is a broken gh, not patience.
const ghAuthTimeout = 15 * time.Second

// GhAuthTokenFn reads the credential from somewhere other than the environment.
// nil means "ask the gh CLI" (ghAuthFromCLI); a test replaces it so a run never
// depends on whose gh happens to be signed in.
type GhAuthTokenFn func(ctx context.Context) (string, error)

// credential finds the token to call GitHub with, and says where it came from.
// The second return is a label for the refusal message; it is non-empty only when
// a token was found.
func (c PullRequestReview) credential(explicit string) (string, string) {
	if token := strings.TrimSpace(explicit); token != "" {
		return token, "the token it was given"
	}
	if token := strings.TrimSpace(os.Getenv(EnvGitHubToken)); token != "" {
		return token, EnvGitHubToken
	}
	if token := strings.TrimSpace(os.Getenv(EnvGitHubTokenAlt)); token != "" {
		return token, EnvGitHubTokenAlt
	}
	from := c.GhAuthToken
	if from == nil {
		from = ghAuthFromCLI
	}
	if token, err := from(context.Background()); err == nil {
		if token = strings.TrimSpace(token); token != "" {
			return token, "the gh CLI"
		}
	}
	return "", ""
}

// noCredential is the refusal when no source had a token: it names every place one
// would have been looked for, so the fix is in the message.
func noCredential() error {
	return fmt.Errorf("%s: no credential (set %s or %s, or sign in with `gh auth login`)",
		ReviewName, EnvGitHubToken, EnvGitHubTokenAlt)
}

// ghAuthFromCLI reads the credential the gh CLI holds.
func ghAuthFromCLI(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghAuthTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gh auth token: no token")
	}
	return token, nil
}
