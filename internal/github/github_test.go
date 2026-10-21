package github

import (
	"encoding/json"
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
)

func TestNextLink(t *testing.T) {
	h := `<https://api.github.com/user/repos?page=2>; rel="next", <https://api.github.com/user/repos?page=5>; rel="last"`
	if got := NextLink(h); got != "https://api.github.com/user/repos?page=2" {
		t.Fatalf("got %q", got)
	}
	if got := NextLink(`<https://x/?page=1>; rel="prev"`); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSearchItemEvent(t *testing.T) {
	var it issueJSON
	raw := `{"number":1,"title":"bug","user":{"login":"a","type":"Bot"},"labels":[{"name":"bug"},{"name":"p1"}],"head":{"ref":"fix"}}`
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatal(err)
	}
	e := it.event("o/r", config.KindPullRequest)
	if !e.Bot || len(e.Labels) != 2 || e.Labels[1] != "p1" || e.Branch != "fix" || e.Key != "o/r:pull_request:1" {
		t.Fatalf("got %+v", e)
	}
}

func TestGraphQLPullRequestEvent(t *testing.T) {
	var page pageJSON
	raw := `{"repository":{"items":{"pageInfo":{"hasNextPage":false},"nodes":[{
	  "number":4,"title":"Add x","url":"u","body":"b","isDraft":false,
	  "author":{"login":"renovate","__typename":"Bot"},
	  "labels":{"nodes":[{"name":"deps"}]},
	  "headRefName":"renovate/x","headRefOid":"abc","mergeable":"CONFLICTING","reviewDecision":"CHANGES_REQUESTED",
	  "reviewRequests":{"nodes":[{"requestedReviewer":{"login":"me"}},{"requestedReviewer":{}}]},
	  "commits":{"nodes":[{"commit":{"author":{"user":{"login":"amy"}},"statusCheckRollup":{"state":"FAILURE"}}}]},
	  "comments":{"nodes":[{"id":"C1","body":"hi","author":{"login":"bob","__typename":"User"}}]},
	  "reviews":{"nodes":[{"id":"R1","state":"CHANGES_REQUESTED","body":"needs work","author":{"login":"cat","__typename":"User"},
	    "comments":{"nodes":[{"body":"rename this","path":"x.go"}]}}]}
	}]}}}`
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	e := page.Repository.Items.Nodes[0].event("o/r", config.KindPullRequest)
	switch {
	case e.Author != "renovate[bot]" || !e.Bot:
		t.Fatalf("bot author should use the REST spelling: %+v", e)
	case e.HeadSHA != "abc" || e.HeadAuthor != "amy" || e.Checks != "FAILURE" || e.Mergeable != "CONFLICTING":
		t.Fatalf("PR state not parsed: %+v", e)
	case len(e.ReviewRequests) != 1 || e.ReviewRequests[0] != "me":
		t.Fatalf("review requests: %v", e.ReviewRequests)
	case e.LastComment.ID != "C1" || e.LastComment.Author != "bob":
		t.Fatalf("last comment: %+v", e.LastComment)
	case e.LastReview.State != "CHANGES_REQUESTED" || e.LastReview.Body != "needs work\n\nx.go: rename this":
		t.Fatalf("last review: %+v", e.LastReview)
	}
}

func TestGraphQLGhostAuthor(t *testing.T) {
	e := itemJSON{Number: 1}.event("o/r", config.KindIssue)
	if e.Author != "ghost" || e.Bot {
		t.Fatalf("deleted accounts should read as ghost: %+v", e)
	}
}

func TestDependabotEvent(t *testing.T) {
	var a dependabotJSON
	raw := `{"number":7,"html_url":"u","dependency":{"package":{"ecosystem":"npm","name":"lodash"},"manifest_path":"package.json"},
	"security_advisory":{"ghsa_id":"GHSA-1","summary":"Prototype pollution","severity":"high"},
	"security_vulnerability":{"vulnerable_version_range":"< 4.17.21","first_patched_version":{"identifier":"4.17.21"}}}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	e := a.event("O/R")
	if e.Title != "lodash: Prototype pollution" || e.Severity != "high" || e.Key != "o/r:dependabot_alert:7" {
		t.Fatalf("got %+v", e)
	}
}

func TestRepoFromAPIURL(t *testing.T) {
	if got := repoFromAPIURL("https://api.github.com/repos/cli/cli"); got != "cli/cli" {
		t.Fatalf("got %q", got)
	}
}
