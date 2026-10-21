package github

import (
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
)

func pr() Event {
	return Event{Kind: config.KindPullRequest, Author: "amy", HeadSHA: "a1", HeadAuthor: "amy", Checks: "SUCCESS",
		Mergeable: "MERGEABLE", LastComment: Activity{ID: "C1", Author: "amy"}, LastReview: Activity{ID: "R1", Author: "bob", State: "APPROVED"}}
}

func triggers(cs []Change) []config.Trigger {
	var out []config.Trigger
	for _, c := range cs {
		out = append(out, c.Trigger)
	}
	return out
}

func TestDetectNothingChanged(t *testing.T) {
	e := pr()
	if got := Detect(e.Snapshot(), e, "me"); len(got) != 0 {
		t.Fatalf("got %v", triggers(got))
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Event)
		want   []config.Trigger
	}{
		{"push by someone else", func(e *Event) { e.HeadSHA = "a2" }, []config.Trigger{config.TriggerPushed}},
		{"push by me", func(e *Event) { e.HeadSHA, e.HeadAuthor = "a2", "ME" }, nil},
		{"checks fail", func(e *Event) { e.Checks = "FAILURE" }, []config.Trigger{config.TriggerChecksFailed}},
		{"push that fails CI", func(e *Event) { e.HeadSHA, e.Checks = "a2", "ERROR" },
			[]config.Trigger{config.TriggerChecksFailed, config.TriggerPushed}},
		{"conflict", func(e *Event) { e.Mergeable = "CONFLICTING" }, []config.Trigger{config.TriggerConflict}},
		{"changes requested", func(e *Event) {
			e.LastReview = Activity{ID: "R2", Author: "bob", State: "CHANGES_REQUESTED", Body: "fix it"}
		}, []config.Trigger{config.TriggerChangesRequested}},
		{"review comment", func(e *Event) { e.LastReview = Activity{ID: "R2", Author: "bob", State: "COMMENTED", Body: "nit"} },
			[]config.Trigger{config.TriggerCommented}},
		{"empty approval", func(e *Event) { e.LastReview = Activity{ID: "R2", Author: "bob", State: "APPROVED"} }, nil},
		{"comment", func(e *Event) { e.LastComment = Activity{ID: "C2", Author: "bob", Body: "?"} }, []config.Trigger{config.TriggerCommented}},
		{"my comment", func(e *Event) { e.LastComment = Activity{ID: "C2", Author: "me"} }, nil},
		{"bot comment", func(e *Event) { e.LastComment = Activity{ID: "C2", Author: "codecov[bot]", Bot: true} }, nil},
		{"review requested", func(e *Event) { e.ReviewRequests = []string{"Me"} }, []config.Trigger{config.TriggerReviewRequested}},
		{"labeled", func(e *Event) { e.Labels = []string{"bug"} }, []config.Trigger{config.TriggerLabeled}},
	}
	for _, tt := range tests {
		e := pr()
		prev := e.Snapshot()
		tt.change(&e)
		got := triggers(Detect(prev, e, "me"))
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			}
		}
	}
}

func TestDetectReadyAndIDs(t *testing.T) {
	e := pr()
	e.Draft = true
	prev := e.Snapshot()
	e.Draft = false
	got := Detect(prev, e, "me")
	if len(got) != 1 || got[0].Trigger != config.TriggerReadyForReview {
		t.Fatalf("got %v", triggers(got))
	}

	// Checks that keep failing on the same commit fire once; a new commit
	// that fails again is a new change.
	e = pr()
	e.Checks = "FAILURE"
	if got := Detect(e.Snapshot(), e, "me"); len(got) != 0 {
		t.Fatalf("still failing on the same commit should not fire: %v", triggers(got))
	}
	prev = e.Snapshot()
	e.HeadSHA, e.HeadAuthor = "a2", "me"
	got = Detect(prev, e, "me")
	if len(got) != 1 || got[0].ID != "checks_failed@a2" {
		t.Fatalf("failing again after my push should fire with the new commit: %+v", got)
	}
}

func TestDetectIssueIgnoresPRTriggers(t *testing.T) {
	e := Event{Kind: config.KindIssue, Checks: "FAILURE", Labels: []string{"bug"}}
	got := Detect(Snapshot{}, e, "me")
	if len(got) != 1 || got[0].Trigger != config.TriggerLabeled {
		t.Fatalf("issues only support their own triggers: %v", triggers(got))
	}
}
