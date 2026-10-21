package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
)

// InboxItem is a notification that asks something of the user: the item it
// is about and the change to act on.
type InboxItem struct {
	Event  Event
	Change Change
}

type notificationJSON struct {
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Type             string `json:"type"`
		URL              string `json:"url"`
		LatestCommentURL string `json:"latest_comment_url"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type commentJSON struct {
	ID   int64    `json:"id"`
	Body string   `json:"body"`
	User userJSON `json:"user"`
}

// Inbox reads participating notifications, read or not, and keeps review
// requests, mentions and assignments on open pull requests and issues that
// still apply to me.
func (r *REST) Inbox(ctx context.Context, me string, since time.Time) ([]InboxItem, error) {
	q := url.Values{"all": {"true"}, "participating": {"true"}, "per_page": {"50"},
		"since": {since.UTC().Format(time.RFC3339)}}
	notes, err := getAll[notificationJSON](ctx, r.c, "notifications?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var out []InboxItem
	for _, n := range notes {
		if inboxKind(n) == "" || !inboxReason(n.Reason) {
			continue
		}
		var subj issueJSON
		if err := r.getJSON(ctx, n.Subject.URL, &subj); err != nil {
			continue // deleted or no longer visible
		}
		var c *commentJSON
		if u := n.Subject.LatestCommentURL; u != "" && u != n.Subject.URL && isMention(n.Reason) {
			c = &commentJSON{}
			if err := r.getJSON(ctx, u, c); err != nil {
				c = nil
			}
		}
		if it, ok := inboxItem(n, subj, c, me); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *REST) getJSON(ctx context.Context, path string, v any) error {
	resp, err := r.c.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func inboxKind(n notificationJSON) config.EventKind {
	switch n.Subject.Type {
	case "PullRequest":
		return config.KindPullRequest
	case "Issue":
		return config.KindIssue
	}
	return ""
}

func inboxReason(r string) bool {
	return r == "review_requested" || r == "assign" || isMention(r)
}

func isMention(r string) bool { return r == "mention" || r == "team_mention" }

// inboxItem turns a notification into an item to act on, checking that it
// still applies: the PR or issue is open, the review is still requested, the
// comment really mentions me, I am still assigned.
func inboxItem(n notificationJSON, subj issueJSON, c *commentJSON, me string) (InboxItem, bool) {
	k := inboxKind(n)
	if subj.State != "open" {
		return InboxItem{}, false
	}
	ev := subj.event(n.Repository.FullName, k)
	is := func(u userJSON) bool { return strings.EqualFold(u.Login, me) }
	var ch Change
	switch {
	case n.Reason == "review_requested":
		if !anyUser(subj.RequestedReviewers, is) && len(subj.RequestedTeams) == 0 {
			return InboxItem{}, false
		}
		ch = Change{Trigger: config.TriggerReviewRequested, ID: "review_requested@" + ev.HeadSHA}
	case n.Reason == "assign":
		if !anyUser(subj.Assignees, is) {
			return InboxItem{}, false
		}
		ch = Change{Trigger: config.TriggerAssigned, ID: "assigned"}
	case c != nil:
		if is(c.User) || (n.Reason == "mention" && !mentions(c.Body, me)) {
			return InboxItem{}, false
		}
		ch = Change{Trigger: config.TriggerMentioned, Actor: c.User.Login, Detail: truncate(c.Body, 2000),
			ID: CommentChangeID(Activity{DatabaseID: c.ID})}
		if !strings.Contains(n.Subject.LatestCommentURL, "/issues/comments/") {
			// Review comments number separately from issue comments.
			ch.ID = "review_comment@" + strconv.FormatInt(c.ID, 10)
		}
	default:
		// Mentioned in the PR or issue description itself.
		if is(subj.User) || (n.Reason == "mention" && !mentions(subj.Body, me)) {
			return InboxItem{}, false
		}
		ch = Change{Trigger: config.TriggerMentioned, ID: "mentioned@body", Actor: ev.Author, Detail: ev.Body}
	}
	return InboxItem{Event: ev, Change: ch}, true
}

func anyUser(us []userJSON, f func(userJSON) bool) bool {
	for _, u := range us {
		if f(u) {
			return true
		}
	}
	return false
}

// mentions reports whether text @-mentions login as a whole word.
func mentions(text, login string) bool {
	re := regexp.MustCompile(`(?i)(^|[^\w/-])@` + regexp.QuoteMeta(login) + `($|[^\w-])`)
	return re.MatchString(text)
}
