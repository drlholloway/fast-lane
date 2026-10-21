package github

import (
	"slices"
	"strings"

	"github.com/drlholloway/fast-lane/internal/config"
)

// Activity is a comment or review on an item.
type Activity struct {
	ID     string `json:"id,omitempty"`
	Author string `json:"author,omitempty"`
	Bot    bool   `json:"bot,omitempty"`
	// State is the review state (APPROVED, CHANGES_REQUESTED, COMMENTED).
	State string `json:"state,omitempty"`
	Body  string `json:"body,omitempty"`
}

// Snapshot is what Fast Lane remembers about an item between polls to tell
// what changed.
type Snapshot struct {
	HeadSHA        string   `json:"head_sha,omitempty"`
	Checks         string   `json:"checks,omitempty"`
	Mergeable      string   `json:"mergeable,omitempty"`
	Draft          bool     `json:"draft,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	ReviewRequests []string `json:"review_requests,omitempty"`
	LastCommentID  string   `json:"last_comment_id,omitempty"`
	LastReviewID   string   `json:"last_review_id,omitempty"`
}

// Snapshot captures the parts of e that Detect compares.
func (e Event) Snapshot() Snapshot {
	return Snapshot{
		HeadSHA:        e.HeadSHA,
		Checks:         e.Checks,
		Mergeable:      e.Mergeable,
		Draft:          e.Draft,
		Labels:         e.Labels,
		ReviewRequests: e.ReviewRequests,
		LastCommentID:  e.LastComment.ID,
		LastReviewID:   e.LastReview.ID,
	}
}

// Change is one trigger firing on an item.
type Change struct {
	Trigger config.Trigger
	// ID identifies this particular change (checks failing on one commit,
	// one comment) so it fires only once even if the state flaps.
	ID string
	// Actor is who caused the change, when known.
	Actor string
	// Detail is the comment text, the labels added, and so on.
	Detail string
}

// Opened is the change for an item seen for the first time.
func Opened(e Event) Change {
	return Change{Trigger: config.TriggerOpened, ID: "opened", Actor: e.Author}
}

// Detect compares e with its previous snapshot and returns what changed, in
// the kind's trigger order. Activity by me never counts, since Claude acts
// as the user, and neither do comments from bots.
func Detect(prev Snapshot, e Event, me string) []Change {
	other := func(login string) bool { return login != "" && !strings.EqualFold(login, me) }
	found := map[config.Trigger]Change{}
	add := func(c Change) {
		if _, ok := found[c.Trigger]; !ok {
			found[c.Trigger] = c
		}
	}

	if e.HeadSHA != "" && prev.HeadSHA != "" && e.HeadSHA != prev.HeadSHA && other(e.HeadAuthor) {
		add(Change{Trigger: config.TriggerPushed, ID: "pushed@" + e.HeadSHA, Actor: e.HeadAuthor, Detail: e.HeadSHA})
	}
	if failed(e.Checks) && !(e.HeadSHA == prev.HeadSHA && failed(prev.Checks)) {
		add(Change{Trigger: config.TriggerChecksFailed, ID: "checks_failed@" + e.HeadSHA, Detail: e.HeadSHA})
	}
	if e.Mergeable == "CONFLICTING" && prev.Mergeable != "CONFLICTING" {
		add(Change{Trigger: config.TriggerConflict, ID: "conflict@" + e.HeadSHA})
	}
	if rv := e.LastReview; rv.ID != "" && rv.ID != prev.LastReviewID && other(rv.Author) && !rv.Bot {
		switch {
		case rv.State == "CHANGES_REQUESTED":
			add(Change{Trigger: config.TriggerChangesRequested, ID: "review@" + rv.ID, Actor: rv.Author, Detail: rv.Body})
		case rv.Body != "":
			add(Change{Trigger: config.TriggerCommented, ID: "review@" + rv.ID, Actor: rv.Author, Detail: rv.Body})
		}
	}
	if c := e.LastComment; c.ID != "" && c.ID != prev.LastCommentID && other(c.Author) && !c.Bot {
		add(Change{Trigger: config.TriggerCommented, ID: "comment@" + c.ID, Actor: c.Author, Detail: c.Body})
	}
	if requested(e.ReviewRequests, me) && !requested(prev.ReviewRequests, me) {
		add(Change{Trigger: config.TriggerReviewRequested, ID: "review_requested@" + e.HeadSHA})
	}
	if prev.Draft && !e.Draft {
		add(Change{Trigger: config.TriggerReadyForReview, ID: "ready@" + e.HeadSHA, Actor: e.Author})
	}
	var added []string
	for _, l := range e.Labels {
		if !slices.ContainsFunc(prev.Labels, func(p string) bool { return strings.EqualFold(p, l) }) {
			added = append(added, l)
		}
	}
	if len(added) > 0 {
		slices.Sort(added)
		add(Change{Trigger: config.TriggerLabeled, ID: "labeled:" + strings.Join(added, ","), Detail: strings.Join(added, ", ")})
	}

	var out []Change
	for _, t := range e.Kind.Triggers() {
		if c, ok := found[t]; ok {
			out = append(out, c)
		}
	}
	return out
}

func failed(checks string) bool { return checks == "FAILURE" || checks == "ERROR" }

func requested(logins []string, me string) bool {
	return me != "" && slices.ContainsFunc(logins, func(l string) bool { return strings.EqualFold(l, me) })
}
