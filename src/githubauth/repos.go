package githubauth

import (
	"net/url"
	"strings"
)

// ParseRepos splits an account's gitRepos field: "kaulie/autonomy, kaulie/other".
func ParseRepos(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		repo := NormalizeRepo(part)
		if repo == "" || seen[strings.ToLower(repo)] {
			continue
		}
		seen[strings.ToLower(repo)] = true
		out = append(out, repo)
	}
	return out
}

// NormalizeRepo accepts owner/name, a github https/ssh URL, or a bare name.
func NormalizeRepo(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")
	if strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
		s = repoFromURL(s)
	}
	s = strings.Trim(s, "/")
	if i := strings.Index(s, "/"); i > 0 {
		owner, name := s[:i], s[i+1:]
		if j := strings.Index(name, "/"); j >= 0 {
			name = name[:j]
		}
		if owner != "" && name != "" {
			return owner + "/" + name
		}
		return ""
	}
	return ""
}

func repoFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "git@") {
		// git@github.com:owner/name.git
		if i := strings.Index(s, ":"); i >= 0 {
			return strings.TrimSuffix(s[i+1:], ".git")
		}
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
}

// RepoName is the repository name without owner (GitHub App token body uses this).
func RepoName(ownerName string) string {
	repo := NormalizeRepo(ownerName)
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// Allows reports whether repo is in the allowlist. An empty allowlist means
// "no extra account restriction" (the GitHub App installation still bounds it).
func Allows(allowlist []string, repo string) bool {
	want := NormalizeRepo(repo)
	if want == "" {
		return len(allowlist) == 0
	}
	if len(allowlist) == 0 {
		return true
	}
	for _, allowed := range allowlist {
		if strings.EqualFold(NormalizeRepo(allowed), want) {
			return true
		}
	}
	return false
}
