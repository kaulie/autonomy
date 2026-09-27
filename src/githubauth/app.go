package githubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvAppID            = "GITHUB_APP_ID"
	EnvInstallationID   = "GITHUB_APP_INSTALLATION_ID"
	EnvPrivateKey       = "GITHUB_APP_PRIVATE_KEY"
	EnvPrivateKeyPath   = "GITHUB_APP_PRIVATE_KEY_PATH"
	EnvAPIURL           = "GITHUB_API_URL"
	defaultAPI          = "https://api.github.com"
	mintHTTPTimeout     = 15 * time.Second
	tokenFileName       = "github-token"
	credentialsFileName = "git-credentials"
)

// Config is the runtime GitHub App. Empty AppID means "not configured".
type Config struct {
	AppID          int64
	InstallationID int64
	PrivateKey     []byte
	APIURL         string
	HTTPClient     *http.Client
	Now            func() time.Time
}

// FromEnv reads the App from the process environment. Missing AppID is not an
// error: workers then keep using whatever git/gh the host already has.
func FromEnv() (Config, error) {
	rawID := strings.TrimSpace(os.Getenv(EnvAppID))
	if rawID == "" {
		return Config{}, nil
	}
	appID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || appID <= 0 {
		return Config{}, fmt.Errorf("%s must be a positive integer", EnvAppID)
	}
	rawInst := strings.TrimSpace(os.Getenv(EnvInstallationID))
	if rawInst == "" {
		return Config{}, fmt.Errorf("%s is required when %s is set", EnvInstallationID, EnvAppID)
	}
	instID, err := strconv.ParseInt(rawInst, 10, 64)
	if err != nil || instID <= 0 {
		return Config{}, fmt.Errorf("%s must be a positive integer", EnvInstallationID)
	}
	pemBytes, err := readPrivateKey()
	if err != nil {
		return Config{}, err
	}
	return Config{
		AppID:          appID,
		InstallationID: instID,
		PrivateKey:     pemBytes,
		APIURL:         strings.TrimRight(strings.TrimSpace(os.Getenv(EnvAPIURL)), "/"),
	}, nil
}

func readPrivateKey() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvPrivateKey)); raw != "" {
		return []byte(strings.ReplaceAll(raw, `\n`, "\n")), nil
	}
	path := strings.TrimSpace(os.Getenv(EnvPrivateKeyPath))
	if path == "" {
		return nil, fmt.Errorf("set %s or %s", EnvPrivateKeyPath, EnvPrivateKey)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvPrivateKeyPath, err)
	}
	return b, nil
}

// Configured reports whether a GitHub App can mint tokens.
func (c Config) Configured() bool {
	return c.AppID > 0 && c.InstallationID > 0 && len(c.PrivateKey) > 0
}

// Mint returns a one-hour installation token, optionally narrowed to repos
// (owner/name). Empty repos = every repository the installation can see.
func (c Config) Mint(ctx context.Context, repos []string) (string, time.Time, error) {
	if !c.Configured() {
		return "", time.Time{}, fmt.Errorf("github app is not configured")
	}
	key, err := parseRSAPrivateKey(c.PrivateKey)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	jwt, err := appJWT(c.AppID, key, now())
	if err != nil {
		return "", time.Time{}, err
	}
	body := map[string]any{
		"permissions": map[string]string{
			"contents":      "write",
			"pull_requests": "write",
			"metadata":      "read",
		},
	}
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if name := RepoName(repo); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		body["repositories"] = names
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", time.Time{}, err
	}
	api := c.APIURL
	if api == "" {
		api = defaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%d/access_tokens", api, c.InstallationID),
		bytes.NewReader(payload))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: mintHTTPTimeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint github app token: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("mint github app token: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("mint github app token: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return "", time.Time{}, fmt.Errorf("mint github app token: empty token")
	}
	exp, err := time.Parse(time.RFC3339, parsed.ExpiresAt)
	if err != nil {
		exp = now().Add(50 * time.Minute)
	}
	return parsed.Token, exp, nil
}

// TokenFor picks a git token for one account: its own PAT, else a minted App
// token narrowed to its allowlist (or to repo if that repo is allowed).
func TokenFor(ctx context.Context, gitToken string, gitRepos []string, repo string) (string, string, error) {
	allow := gitRepos
	if repo != "" && !Allows(allow, repo) {
		return "", "", fmt.Errorf("account gitRepos does not include %s", NormalizeRepo(repo))
	}
	if pat := strings.TrimSpace(gitToken); pat != "" {
		return pat, "account git token", nil
	}
	cfg, err := FromEnv()
	if err != nil {
		return "", "", err
	}
	if !cfg.Configured() {
		return "", "", nil
	}
	repos := allow
	if repo != "" {
		repos = []string{NormalizeRepo(repo)}
	}
	token, _, err := cfg.Mint(ctx, repos)
	if err != nil {
		return "", "", err
	}
	return token, "github app", nil
}
