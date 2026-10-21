// Package local finds checkouts of GitHub repositories on this machine, so
// sessions can work from them instead of cloning.
package local

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Checkout is a local clone of a GitHub repository.
type Checkout struct {
	Dir string `json:"dir"`
	// Remote is the git remote that points at the repository.
	Remote string `json:"remote"`
}

// MaxDepth is how many directory levels below each code dir Scan looks.
const MaxDepth = 3

// skipDirs are never descended into.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true}

// Scan looks up to MaxDepth levels below each of dirs for git checkouts and
// maps each GitHub repository ("owner/name", lower case) to one of them. When
// several checkouts match, a remote named origin beats upstream beats any
// other, then a directory named after the repo, then the shorter path.
func Scan(dirs []string) map[string]Checkout {
	type found struct {
		Checkout
		rank int
	}
	best := map[string]found{}
	consider := func(repo string, c found) {
		cur, ok := best[repo]
		if !ok || better(repo, c.Checkout, c.rank, cur.Checkout, cur.rank) {
			best[repo] = c
		}
	}
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || seen[strings.ToLower(real)] {
			return
		}
		// Lower case because macOS treats ~/Code and ~/code as one folder.
		seen[strings.ToLower(real)] = true
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			for remote, repo := range remotes(filepath.Join(dir, ".git", "config")) {
				consider(repo, found{Checkout{Dir: dir, Remote: remote}, remoteRank(remote)})
			}
		}
		if depth == MaxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	for _, d := range dirs {
		walk(d, 0)
	}
	out := make(map[string]Checkout, len(best))
	for repo, f := range best {
		out[repo] = f.Checkout
	}
	return out
}

func remoteRank(name string) int {
	switch name {
	case "origin":
		return 0
	case "upstream":
		return 1
	}
	return 2
}

func better(repo string, a Checkout, ar int, b Checkout, br int) bool {
	if ar != br {
		return ar < br
	}
	_, name, _ := strings.Cut(repo, "/")
	an, bn := strings.EqualFold(filepath.Base(a.Dir), name), strings.EqualFold(filepath.Base(b.Dir), name)
	if an != bn {
		return an
	}
	if len(a.Dir) != len(b.Dir) {
		return len(a.Dir) < len(b.Dir)
	}
	return a.Dir < b.Dir
}

var sectionRE = regexp.MustCompile(`^\[remote\s+"([^"]+)"\]$`)

// remotes reads a git config file and returns the GitHub repository each
// remote points at, by remote name.
func remotes(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	remote := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			remote = ""
			if m := sectionRE.FindStringSubmatch(line); m != nil {
				remote = m[1]
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if remote == "" || !ok || strings.TrimSpace(k) != "url" {
			continue
		}
		if repo, ok := GitHubRepo(strings.TrimSpace(v)); ok {
			out[remote] = repo
		}
	}
	return out
}

var urlRE = regexp.MustCompile(`^(?:(?:https?|git|ssh)://(?:[^@/]+@)?github\.com(?::\d+)?/|[^@/]+@github\.com:)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// GitHubRepo extracts "owner/name" (lower case) from a GitHub remote URL in
// any of the forms git accepts: git@github.com:o/r.git, ssh://, https://.
func GitHubRepo(url string) (string, bool) {
	m := urlRE.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1] + "/" + m[2]), true
}

// Open describes the checkout at dir for repo, finding the remote that
// points at it. It assumes origin when no remote matches.
func Open(dir, repo string) Checkout {
	c, rank := Checkout{Dir: dir, Remote: "origin"}, -1
	for remote, r := range remotes(filepath.Join(dir, ".git", "config")) {
		if strings.EqualFold(r, repo) && (rank < 0 || remoteRank(remote) < rank || remoteRank(remote) == rank && remote < c.Remote) {
			c.Remote, rank = remote, remoteRank(remote)
		}
	}
	return c
}
