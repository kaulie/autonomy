package githubauth

import "testing"

func TestNormalizeRepo(t *testing.T) {
	cases := map[string]string{
		"kaulie/autonomy":                        "kaulie/autonomy",
		"https://github.com/kaulie/autonomy.git": "kaulie/autonomy",
		"git@github.com:kaulie/autonomy.git":     "kaulie/autonomy",
		"  kaulie/autonomy/  ":                   "kaulie/autonomy",
		"":                                       "",
		"autonomy":                               "",
	}
	for in, want := range cases {
		if got := NormalizeRepo(in); got != want {
			t.Fatalf("NormalizeRepo(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseReposAndAllows(t *testing.T) {
	got := ParseRepos("kaulie/autonomy, kaulie/other")
	if len(got) != 2 || got[0] != "kaulie/autonomy" || got[1] != "kaulie/other" {
		t.Fatalf("ParseRepos=%v", got)
	}
	if !Allows(got, "https://github.com/kaulie/autonomy") {
		t.Fatal("should allow autonomy")
	}
	if Allows(got, "kaulie/secret") {
		t.Fatal("should refuse secret")
	}
	if !Allows(nil, "kaulie/anything") {
		t.Fatal("empty allowlist is no extra restriction")
	}
}

func TestTokenForRefusesOutsideAllowlist(t *testing.T) {
	_, _, err := TokenFor(t.Context(), "pat-x", []string{"kaulie/autonomy"}, "kaulie/other")
	if err == nil {
		t.Fatal("want refusal")
	}
}

func TestTokenForUsesAccountPAT(t *testing.T) {
	token, src, err := TokenFor(t.Context(), "pat-x", []string{"kaulie/autonomy"}, "kaulie/autonomy")
	if err != nil || token != "pat-x" || src != "account git token" {
		t.Fatalf("token=%q src=%q err=%v", token, src, err)
	}
}
