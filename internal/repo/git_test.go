package repo

import "testing"

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://github.com/foo/bar.git", "https://github.com/foo/bar.git", false},
		{"https://github.com/foo/bar", "https://github.com/foo/bar.git", false},
		{"github.com/foo/bar", "https://github.com/foo/bar.git", false},
		{"https://user:pw@github.com/foo/bar.git", "https://github.com/foo/bar.git", false},
		{"https://github.com/foo/bar.git?ref=main#x", "https://github.com/foo/bar.git", false},
		{"git@github.com:foo/bar.git", "", true},
		{"ssh://git@github.com/foo/bar.git", "", true},
		{"http://github.com/foo/bar.git", "", true},
		{"https://github.com/", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := normalizeURL(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err: got %v, wantErr=%v", err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAuthedURL(t *testing.T) {
	got, err := authedURL("https://github.com/foo/bar.git", "ghp_secret")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://x-access-token:ghp_secret@github.com/foo/bar.git"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAliasFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/foo/bar.git":        "bar",
		"https://github.com/foo/bar":            "bar",
		"https://example.org/nested/path/proj":  "proj",
	}
	for in, want := range cases {
		if got := aliasFromURL(in); got != want {
			t.Errorf("aliasFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateBranchName(t *testing.T) {
	valid := []string{
		"main",
		"feature/foo",
		"fix-123",
		"release/v1.2.3",
		"a",
	}
	for _, n := range valid {
		if err := ValidateBranchName(n); err != nil {
			t.Errorf("expected valid: %q, got %v", n, err)
		}
	}

	invalid := []string{
		"",
		"-leading-dash",
		"has spaces",
		"has;semicolon",
		"weird..dots",
		"branch.lock",
		"/starts-slash",
		"..",
		"contains\nnewline",
	}
	for _, n := range invalid {
		if err := ValidateBranchName(n); err == nil {
			t.Errorf("expected invalid: %q", n)
		}
	}
}

func TestScrubPAT(t *testing.T) {
	in := "clone failed: https://x-access-token:ghp_abc@example.com/foo.git bad credentials"
	out := scrubPAT(errString(in), "ghp_abc").Error()
	if contains(out, "ghp_abc") {
		t.Fatalf("PAT leaked in %q", out)
	}
	if !contains(out, "***") {
		t.Fatalf("expected *** in %q", out)
	}
}

// small local helpers to avoid importing errors/strings just for tests
type errString string

func (e errString) Error() string { return string(e) }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
