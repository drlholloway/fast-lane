package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubRepo(t *testing.T) {
	tests := map[string]string{
		"git@github.com:Owner/Repo.git":              "owner/repo",
		"git@github.com:owner/repo":                  "owner/repo",
		"https://github.com/owner/my.repo.git":       "owner/my.repo",
		"https://user@github.com/owner/repo/":        "owner/repo",
		"ssh://git@github.com/owner/repo.git":        "owner/repo",
		"ssh://git@github.com:22/owner/repo.git":     "owner/repo",
		"git://github.com/owner/repo.git":            "owner/repo",
		"https://gitlab.com/owner/repo.git":          "",
		"git@github.com.evil.example:owner/repo.git": "",
		"/local/path/repo.git":                       "",
	}
	for url, want := range tests {
		got, ok := GitHubRepo(url)
		if got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", url, got, ok, want)
		}
	}
}

func gitRepo(t *testing.T, dir string, remotes map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tbare = false\n"
	for name, url := range remotes {
		cfg += "[remote \"" + name + "\"]\n\turl = " + url + "\n\tfetch = +refs/heads/*:refs/remotes/" + name + "/*\n"
	}
	cfg += "[branch \"main\"]\n\tremote = origin\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, filepath.Join(root, "app"), map[string]string{"origin": "git@github.com:me/app.git"})
	// A fork: origin is mine, upstream is theirs.
	gitRepo(t, filepath.Join(root, "lib"), map[string]string{
		"origin": "https://github.com/me/lib.git", "upstream": "https://github.com/them/lib.git"})
	// Another clone of them/lib where it is origin wins over the fork's upstream.
	gitRepo(t, filepath.Join(root, "work", "them-lib"), map[string]string{"origin": "git@github.com:them/lib.git"})
	// Two origin clones of me/app: the one named after the repo wins.
	gitRepo(t, filepath.Join(root, "old", "app-copy"), map[string]string{"origin": "git@github.com:me/app.git"})
	// Too deep, inside node_modules, and hidden: all skipped.
	gitRepo(t, filepath.Join(root, "a", "b", "c", "deep"), map[string]string{"origin": "git@github.com:me/deep.git"})
	gitRepo(t, filepath.Join(root, "web", "node_modules", "dep"), map[string]string{"origin": "git@github.com:x/dep.git"})
	gitRepo(t, filepath.Join(root, ".cache", "hid"), map[string]string{"origin": "git@github.com:x/hid.git"})

	got := Scan([]string{root, root, filepath.Join(root, "missing")})
	want := map[string]Checkout{
		"me/app":   {Dir: filepath.Join(root, "app"), Remote: "origin"},
		"me/lib":   {Dir: filepath.Join(root, "lib"), Remote: "origin"},
		"them/lib": {Dir: filepath.Join(root, "work", "them-lib"), Remote: "origin"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for repo, c := range want {
		if got[repo] != c {
			t.Errorf("%s: got %+v, want %+v", repo, got[repo], c)
		}
	}
}
