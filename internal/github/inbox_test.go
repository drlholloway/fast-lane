package github

import (
	"encoding/json"
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
)

func note(t *testing.T, raw string) notificationJSON {
	t.Helper()
	var n notificationJSON
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func subject(t *testing.T, raw string) issueJSON {
	t.Helper()
	var it issueJSON
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestInboxReviewRequest(t *testing.T) {
	n := note(t, `{"reason":"review_requested","subject":{"type":"PullRequest","url":"u"},"repository":{"full_name":"them/lib"}}`)
	pr := subject(t, `{"number":8,"title":"Add y","state":"open","user":{"login":"amy"},"head":{"ref":"y","sha":"h1"},
	  "requested_reviewers":[{"login":"Me"}]}`)
	it, ok := inboxItem(n, pr, nil, "me")
	if !ok || it.Change.Trigger != config.TriggerReviewRequested || it.Change.ID != "review_requested@h1" ||
		it.Event.Key != "them/lib:pull_request:8" || it.Event.HeadSHA != "h1" {
		t.Fatalf("got %+v %v", it, ok)
	}

	pr.RequestedReviewers = nil
	if _, ok := inboxItem(n, pr, nil, "me"); ok {
		t.Fatal("a review that is no longer requested should be dropped")
	}
	pr.RequestedTeams = []struct{}{{}}
	if _, ok := inboxItem(n, pr, nil, "me"); !ok {
		t.Fatal("team review requests should count")
	}
	pr.State = "closed"
	if _, ok := inboxItem(n, pr, nil, "me"); ok {
		t.Fatal("closed PRs should be dropped")
	}
}

func TestInboxMentions(t *testing.T) {
	n := note(t, `{"reason":"mention","subject":{"type":"Issue","url":"u",
	  "latest_comment_url":"https://api.github.com/repos/them/lib/issues/comments/55"},"repository":{"full_name":"them/lib"}}`)
	issue := subject(t, `{"number":9,"title":"Q","state":"open","user":{"login":"amy"},"body":"no mention here"}`)

	it, ok := inboxItem(n, issue, &commentJSON{ID: 55, Body: "what do you think @me?", User: userJSON{Login: "bob"}}, "me")
	if !ok || it.Change.Trigger != config.TriggerMentioned || it.Change.ID != "comment@55" || it.Change.Actor != "bob" {
		t.Fatalf("got %+v %v", it, ok)
	}
	if _, ok := inboxItem(n, issue, &commentJSON{ID: 56, Body: "thanks @me-bot", User: userJSON{Login: "bob"}}, "me"); ok {
		t.Fatal("a mention of a longer login is not a mention of me")
	}
	if _, ok := inboxItem(n, issue, &commentJSON{ID: 57, Body: "cc @me", User: userJSON{Login: "ME"}}, "me"); ok {
		t.Fatal("my own comments should not fire")
	}

	n.Subject.LatestCommentURL = "https://api.github.com/repos/them/lib/pulls/comments/77"
	it, _ = inboxItem(n, issue, &commentJSON{ID: 77, Body: "@me look", User: userJSON{Login: "bob"}}, "me")
	if it.Change.ID != "review_comment@77" {
		t.Fatalf("review comments need their own ID space: %q", it.Change.ID)
	}

	n.Subject.LatestCommentURL = ""
	if _, ok := inboxItem(n, issue, nil, "me"); ok {
		t.Fatal("a body without a mention should not fire")
	}
	issue.Body = "Hey @me, can you look?"
	if it, ok := inboxItem(n, issue, nil, "me"); !ok || it.Change.ID != "mentioned@body" || it.Change.Actor != "amy" {
		t.Fatalf("a mention in the body should fire: %+v %v", it, ok)
	}
}

func TestInboxAssign(t *testing.T) {
	n := note(t, `{"reason":"assign","subject":{"type":"Issue","url":"u"},"repository":{"full_name":"them/lib"}}`)
	issue := subject(t, `{"number":3,"state":"open","assignees":[{"login":"me"}]}`)
	if it, ok := inboxItem(n, issue, nil, "me"); !ok || it.Change.Trigger != config.TriggerAssigned {
		t.Fatalf("got %+v %v", it, ok)
	}
	issue.Assignees = nil
	if _, ok := inboxItem(n, issue, nil, "me"); ok {
		t.Fatal("unassigned should be dropped")
	}
}

func TestCommentChangeIDMatchesInbox(t *testing.T) {
	// The same comment seen by repo polling and by the inbox fires once.
	if got := CommentChangeID(Activity{ID: "IC_x", DatabaseID: 55}); got != "comment@55" {
		t.Fatalf("got %q", got)
	}
}
