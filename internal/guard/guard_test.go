package guard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlocked(t *testing.T) {
	blocked := []string{
		// The repository itself.
		"gh repo delete acme/api --yes",
		"gh repo delete",
		"GH_TOKEN=x gh repo delete acme/api",
		"gh api -X DELETE repos/acme/api",
		"gh api repos/acme/api -X DELETE",
		"gh api repos/acme/api --method=DELETE",
		"gh api -XDELETE /repos/acme/api/",
		"gh api --method delete https://api.github.com/repos/acme/api",
		"gh api repos/acme/api/transfer -f new_owner=evil",
		"gh api repos/acme/api -X PATCH -f default_branch=gone",
		`gh api graphql -f query='mutation { deleteRepository(input: {repositoryId: "R_1"}) { clientMutationId } }'`,
		`gh api graphql -f query='mutation{transferRepository(input:{})}'`,
		"curl -X DELETE -H 'Authorization: token x' https://api.github.com/repos/acme/api",
		"curl --request delete https://api.github.com/repos/acme/api",
		"http DELETE https://api.github.com/repos/acme/api",

		// The default branch.
		"git push --force origin main",
		"git push -f origin HEAD:main",
		"git push origin +main",
		"git push origin +HEAD:refs/heads/master",
		"git push --force-with-lease origin trunk",
		"git push -fu origin release",
		"git push --force",
		"git push -f origin",
		"git push --force origin HEAD",
		"git push --delete origin main",
		"git push origin :main",
		"git push origin --delete refs/heads/master",
		"git push --mirror origin",
		"git push --prune origin",
		"git push --force --all origin",
		"gh api -X DELETE repos/acme/api/git/refs/heads/main",
		"gh api -X PATCH repos/acme/api/git/refs/heads/main -F force=true -f sha=abc",
		"gh repo sync --force",
		`gh api graphql -f query='mutation { deleteRef(input: {refId: "x"}) { clientMutationId } }'`,

		// Tags and releases.
		"git push --force --tags",
		"git push --delete origin v1.2.0",
		"git push origin :refs/tags/v1.0",
		"git push origin --delete tag v1.0",
		"git push -f origin refs/tags/v2.0.0",
		"gh api -X DELETE repos/acme/api/git/refs/tags/v1.0.0",
		"gh release delete v1.0.0 --yes",
		"gh api -X DELETE repos/acme/api/releases/123",

		// Rewriting history, and the guards themselves.
		"git filter-branch --tree-filter 'rm -f secrets' HEAD",
		"git filter-repo --path secrets --invert-paths",
		"git -C /tmp/x filter-repo --force",
		"java -jar bfg-1.14.0.jar --delete-files secrets",
		"bfg --strip-blobs-bigger-than 1M",
		"gh api -X DELETE repos/acme/api/rulesets/42",
		"gh api -X PUT repos/acme/api/branches/main/protection --input p.json",
		"gh ruleset delete 42",

		// Hidden in compound commands, substitutions and wrappers.
		"go test ./... && git push --force origin main",
		"echo ok; gh repo delete acme/api --yes",
		"true || git push origin :main",
		"cd /tmp && sudo -E git push -f origin main",
		"env -i PATH=/bin git push --force origin main",
		"timeout 30 git push -f origin main",
		"/usr/bin/git push --force origin main",
		"git -c user.name=x push --force origin main",
		`bash -c "git push --force origin main"`,
		`sh -c 'cd repo && gh repo delete acme/api --yes'`,
		`eval "gh api -X DELETE repos/acme/api"`,
		"echo $(gh repo delete acme/api --yes)",
		"echo `git push -f origin main`",
		"git push --force origin main\necho done",
		"git push origin +main 2>&1 | tail -1",
	}
	for _, c := range blocked {
		if why := Command(c, "trunk", "release"); why == "" {
			t.Errorf("should block: %s", c)
		}
	}
}

func TestAllowed(t *testing.T) {
	allowed := []string{
		// Ordinary work, including on PR branches.
		"git push origin HEAD:search-rate-limit",
		"git push -u origin fix-ci",
		"git push origin main",
		"git push",
		"git push --force origin fix-ci",
		"git push --force-with-lease origin HEAD:refs/heads/fix-ci",
		"git push origin +feature",
		"git push --delete origin old-feature",
		"git push origin :merged-branch",
		"git push --dry-run --force origin main",
		"git push -nf origin main",
		"git push origin v2-redesign",
		"git push --tags",
		"git push origin tag v1.3.0",
		"gh pr merge 88 --squash --delete-branch",
		"gh pr review 12 --approve --body 'Looks good'",
		"gh pr review 12 --request-changes --body 'Please handle unknown profiles'",
		"gh pr close 12 --comment 'Superseded by #13'",
		"gh issue close 5",
		"gh pr checkout 142",
		"gh repo view acme/api",
		"gh repo clone acme/api",
		"gh repo sync",
		"gh release create v1.3.0 --generate-notes",
		"gh release view v1.0.0",
		"gh api repos/acme/api",
		"gh api repos/acme/api/pulls/12/comments",
		"gh api --method POST repos/acme/api/pulls/12/comments/99/replies -f body='Done'",
		"gh api -X DELETE repos/acme/api/git/refs/heads/merged-branch",
		"gh api -X PATCH repos/acme/api/dependabot/alerts/3 -f state=dismissed",
		"gh api -X DELETE repos/acme/api/releases/assets/7",
		"gh api repos/acme/api/git/refs -f ref=refs/tags/v1.3.0 -f sha=abc",
		"gh api graphql -f query='{ viewer { login } }'",
		"git rebase origin/main && git push --force-with-lease origin HEAD:fix-ci",
		"git reset --hard origin/main",
		"git tag -d v1.0.0",
		"git branch -D old",
		"curl https://api.github.com/repos/acme/api",
		"go test ./...",
		"rm -rf node_modules",
	}
	for _, c := range allowed {
		if why := Command(c, "trunk"); why != "" {
			t.Errorf("should allow: %s (refused: %s)", c, why)
		}
	}
}

func TestCheckInput(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": "gh repo delete acme/api --yes"})
	if Check("Bash", in) == "" {
		t.Fatal("Bash input should be checked")
	}
	if Check("Edit", json.RawMessage(`{"file_path":"x"}`)) != "" {
		t.Fatal("other tools pass")
	}
	if Check("Bash", json.RawMessage(`not json`)) != "" {
		t.Fatal("unreadable input passes; Claude Code would reject it anyway")
	}
}

func TestReasonsSayWhat(t *testing.T) {
	for cmd, want := range map[string]string{
		"gh repo delete acme/api":               "deletes the repository",
		"git push --force origin main":          "force-pushes the default branch main",
		"git push origin :v1.0.0":               "deletes the tag v1.0.0",
		"git push --force":                      "name it explicitly",
		"git filter-repo --path x":              "rewrites the repository's history",
		"gh api -X DELETE repos/a/b/rulesets/1": "rulesets",
	} {
		if got := Command(cmd); !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want it to mention %q", cmd, got, want)
		}
	}
}

func TestWrappersWithFlagValues(t *testing.T) {
	for _, c := range []string{
		"nice -n 10 git push -f origin main",
		"sudo -u deploy git push --force origin main",
		"xargs -n 1 -P 4 gh repo delete",
		"parallel --jobs 2 git push -f origin main ::: x",
		"watch -n 5 gh api -X DELETE repos/acme/api",
	} {
		if Command(c) == "" {
			t.Errorf("should block: %s", c)
		}
	}
}

// The guard errs toward refusing. Text that spells out a destructive command
// is refused too; Claude is told why and can word it differently.
func TestConservative(t *testing.T) {
	for _, c := range []string{
		"git commit -m 'Do not gh repo delete anything'",
		"echo git push -f origin main",
	} {
		if Command(c) == "" {
			t.Errorf("expected the guard to err toward refusing: %s", c)
		}
	}
}
