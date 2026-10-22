// Package guard keeps full-auto sessions from destroying a repository or its
// history. Fast Lane runs every command a full-auto session wants through
// Check before allowing it; only a human may do what Check refuses.
//
// Check reads commands the way a shell would: quotes, &&, ;, pipes, $(...),
// bash -c and wrappers such as env or sudo. It cannot see inside a script
// file, so it is one layer of three: GitHub rulesets on the default branch
// and tags, and a token without the delete_repo scope, cover the rest.
package guard

import (
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Check returns why the tool call would delete or transfer a repository, or
// destroy its history, or "" when it would not. defaultBranches are the
// repository's default branch names; main and master always count.
func Check(tool string, input json.RawMessage, defaultBranches ...string) string {
	if tool != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return Command(in.Command, defaultBranches...)
}

// Command is Check for a shell command line.
func Command(cmd string, defaultBranches ...string) string {
	g := guard{defaults: append([]string{"main", "master"}, defaultBranches...)}
	return g.line(cmd, 0)
}

type guard struct{ defaults []string }

// maxDepth bounds how deep bash -c "…" nesting is followed.
const maxDepth = 4

func (g guard) line(cmd string, depth int) string {
	if depth > maxDepth {
		return ""
	}
	for _, words := range split(cmd) {
		if why := g.simple(words); why != "" {
			return why
		}
		// A quoted word may itself be a command: bash -c "…", eval "…",
		// ssh host "…". Check those too.
		for _, w := range words {
			if strings.ContainsAny(w, " \t\n;&|") {
				if why := g.line(w, depth+1); why != "" {
					return why
				}
			}
		}
	}
	return ""
}

// split turns a command line into simple commands, each a list of words with
// quotes removed. Operators, newlines and command substitution end a command.
func split(s string) [][]string {
	var (
		cmds  [][]string
		words []string
		word  strings.Builder
		inW   bool
		quote rune
	)
	endWord := func() {
		if inW {
			words = append(words, word.String())
			word.Reset()
			inW = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs):
				i++
				word.WriteRune(rs[i])
			default:
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inW = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inW = true
		case r == ' ' || r == '\t':
			endWord()
		case strings.ContainsRune(";&|()\n`", r):
			endCmd()
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endCmd()
			i++
		default:
			word.WriteRune(r)
			inW = true
		}
	}
	endCmd()
	return cmds
}

// simple checks one command. Rather than know every wrapper (env, sudo -u,
// nice -n, xargs, timeout…) and its flags, it checks from every word that
// names a risky program, so a wrapped command is checked the same way.
func (g guard) simple(w []string) string {
	for i, word := range w {
		if why := g.from(path.Base(word), w[i:]); why != "" {
			return why
		}
	}
	return ""
}

// from checks w as a command run by prog, its first word.
func (g guard) from(prog string, w []string) string {
	switch prog {
	case "git":
		return g.git(w[1:])
	case "gh":
		return g.gh(w[1:])
	case "git-filter-repo", "bfg":
		return "rewrites the repository's history"
	case "java":
		if slices.ContainsFunc(w, func(a string) bool { return strings.Contains(strings.ToLower(a), "bfg") }) {
			return "rewrites the repository's history"
		}
	case "curl", "wget", "http", "https", "xh":
		return httpClient(w)
	}
	return ""
}

// git skips git's global options and checks the subcommand.
func (g guard) git(args []string) string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-C" || args[0] == "-c") && len(args) > 1 {
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "filter-branch", "filter-repo":
		return "rewrites the repository's history"
	case "push":
		return g.push(args[1:])
	}
	return ""
}

func (g guard) isDefault(ref string) bool {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return slices.ContainsFunc(g.defaults, func(d string) bool { return strings.EqualFold(d, ref) })
}

var versionRE = regexp.MustCompile(`^v?\d+(\.\d+)+`)

func isTag(ref string) bool {
	return strings.HasPrefix(ref, "refs/tags/") || versionRE.MatchString(ref)
}

// push refuses pushes that could overwrite or delete the default branch or
// tags. Ordinary pushes, force-pushes to other named branches, and deleting
// other branches are allowed.
func (g guard) push(args []string) string {
	var force, del, all, tags, dryRun bool
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--mirror":
			return "pushes a mirror, which can overwrite or delete every branch and tag"
		case a == "--prune":
			return "prunes the remote, which can delete branches and tags"
		case a == "--force" || strings.HasPrefix(a, "--force-with-lease") || a == "--force-if-includes":
			force = true
		case a == "--delete":
			del = true
		case a == "--all" || a == "--branches":
			all = true
		case a == "--tags":
			tags = true
		case a == "-o" || a == "--push-option" || a == "--repo" || a == "--receive-pack" || a == "--exec":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A cluster of short flags, such as -fu.
			dryRun = dryRun || strings.ContainsRune(a, 'n')
			force = force || strings.ContainsRune(a, 'f')
			del = del || strings.ContainsRune(a, 'd')
		default:
			pos = append(pos, a)
		}
	}
	if dryRun {
		return ""
	}
	var specs []string
	if len(pos) > 1 {
		specs = pos[1:]
	}
	// "git push origin tag v1" pushes refs/tags/v1.
	for i := 0; i < len(specs); i++ {
		if specs[i] == "tag" && i+1 < len(specs) {
			specs[i+1] = "refs/tags/" + specs[i+1]
			specs = append(specs[:i], specs[i+1:]...)
		}
	}
	if force && (all || tags) {
		return "force-pushes every branch or tag, which can overwrite the default branch and tags"
	}
	if force && len(specs) == 0 {
		return "force-pushes without naming the branch; name it explicitly, for example git push --force origin HEAD:my-branch"
	}
	for _, spec := range specs {
		plus := strings.HasPrefix(spec, "+")
		spec = strings.TrimPrefix(spec, "+")
		src, dst, hasDst := strings.Cut(spec, ":")
		if !hasDst {
			dst = src
		}
		switch {
		case del || (hasDst && src == ""):
			if g.isDefault(dst) {
				return "deletes the default branch " + strings.TrimPrefix(dst, "refs/heads/")
			}
			if isTag(dst) {
				return "deletes the tag " + strings.TrimPrefix(dst, "refs/tags/")
			}
		case force || plus:
			if dst == "HEAD" || dst == "@" {
				return "force-pushes HEAD without naming the branch; name it, for example git push --force origin HEAD:my-branch"
			}
			if g.isDefault(dst) {
				return "force-pushes the default branch " + strings.TrimPrefix(dst, "refs/heads/")
			}
			if isTag(dst) {
				return "force-pushes the tag " + strings.TrimPrefix(dst, "refs/tags/")
			}
		}
	}
	return ""
}

// ghAPIValueFlags take a value as the next word.
var ghAPIValueFlags = map[string]bool{
	"-X": true, "--method": true, "-H": true, "--header": true, "-f": true, "-F": true,
	"--field": true, "--raw-field": true, "--input": true, "-q": true, "--jq": true,
	"-t": true, "--template": true, "-p": true, "--preview": true, "--hostname": true, "--cache": true,
}

func (g guard) gh(args []string) string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-R" || args[0] == "--repo" {
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) < 2 {
		return ""
	}
	switch args[0] {
	case "repo":
		switch args[1] {
		case "delete":
			return "deletes the repository"
		case "sync":
			if slices.Contains(args, "--force") {
				return "force-syncs a branch, which can overwrite its history"
			}
		}
	case "release":
		if args[1] == "delete" {
			return "deletes a release"
		}
	case "ruleset":
		if args[1] == "delete" || args[1] == "edit" {
			return "changes the repository's rulesets"
		}
	case "api":
		return g.ghAPI(args[1:])
	}
	return ""
}

var repoPathRE = regexp.MustCompile(`^repos/[^/]+/[^/]+`)

func (g guard) ghAPI(args []string) string {
	method, endpoint := "", ""
	hasFields := false
	var text strings.Builder
	for i := 0; i < len(args); i++ {
		a := args[i]
		text.WriteString(strings.ToLower(a) + " ")
		switch {
		case a == "-X" || a == "--method":
			if i+1 < len(args) {
				method = args[i+1]
			}
		case strings.HasPrefix(a, "--method="):
			method = strings.TrimPrefix(a, "--method=")
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = a[2:]
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input":
			hasFields = true
		}
		if ghAPIValueFlags[a] {
			if i+1 < len(args) {
				text.WriteString(strings.ToLower(args[i+1]) + " ")
			}
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") && endpoint == "" {
			endpoint = a
		}
	}
	method = strings.ToUpper(method)
	if method == "" {
		method = "GET"
		if hasFields {
			method = "POST"
		}
	}
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://api.github.com"), "/")
	endpoint, _, _ = strings.Cut(endpoint, "?")
	if endpoint == "graphql" {
		q := strings.ReplaceAll(text.String(), " ", "")
		for _, m := range []string{"deleterepository", "transferrepository", "deleteref(", "deleteref{"} {
			if strings.Contains(q, m) {
				return "runs a GraphQL mutation that deletes or transfers a repository or a ref"
			}
		}
		if strings.Contains(q, "updaterefs") || strings.Contains(q, "updateref(") && strings.Contains(q, "force:true") {
			return "runs a GraphQL mutation that can force-update refs"
		}
		return ""
	}
	return g.rest(method, strings.TrimSuffix(endpoint, "/"), text.String())
}

// rest checks a GitHub REST call.
func (g guard) rest(method, endpoint, text string) string {
	repo := repoPathRE.FindString(endpoint)
	if repo == "" || method == "GET" || method == "HEAD" {
		return ""
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(endpoint, repo), "/")
	switch {
	case rest == "" && method == "DELETE":
		return "deletes the repository"
	case rest == "" && method == "PATCH" && strings.Contains(text, "default_branch"):
		return "changes the repository's default branch"
	case rest == "transfer":
		return "transfers the repository"
	case strings.HasPrefix(rest, "git/refs/heads/") || strings.HasPrefix(rest, "git/refs/tags/"):
		ref := strings.TrimPrefix(rest, "git/")
		if strings.HasPrefix(ref, "refs/tags/") {
			return "deletes or moves the tag " + strings.TrimPrefix(ref, "refs/tags/")
		}
		if g.isDefault(ref) {
			return "deletes or rewrites the default branch " + strings.TrimPrefix(ref, "refs/heads/")
		}
	case strings.HasPrefix(rest, "releases/") && method == "DELETE" && !strings.HasPrefix(rest, "releases/assets/"):
		return "deletes a release"
	case strings.HasPrefix(rest, "rulesets") || strings.HasPrefix(rest, "branches/") && strings.Contains(rest, "/protection"):
		return "changes the repository's rulesets or branch protection"
	}
	return ""
}

// httpClient refuses DELETE requests and transfers sent straight to the
// GitHub API; Claude should use gh, which the guard can read.
func httpClient(w []string) string {
	api := slices.ContainsFunc(w, func(a string) bool { return strings.Contains(a, "api.github.com/repos/") })
	if !api {
		return ""
	}
	for i, a := range w {
		up := strings.ToUpper(a)
		if up == "DELETE" || up == "-XDELETE" || strings.HasSuffix(up, "=DELETE") ||
			(a == "-X" || a == "--request") && i+1 < len(w) && strings.EqualFold(w[i+1], "DELETE") {
			return "sends a DELETE request straight to the GitHub API; use gh instead"
		}
		if strings.Contains(a, "/transfer") {
			return "transfers the repository"
		}
	}
	return ""
}
