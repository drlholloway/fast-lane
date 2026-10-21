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

func TestIssuesDropPullRequests(t *testing.T) {
	var items []issueJSON
	raw := `[{"number":1,"title":"bug","user":{"login":"a"}},
	         {"number":2,"title":"pr","pull_request":{}}]`
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	evs := issuesToEvents("o/r", config.KindIssue, items)
	if len(evs) != 1 || evs[0].Number != 1 || evs[0].Author != "a" {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Key != "o/r:issue:1" {
		t.Fatalf("key %q", evs[0].Key)
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
