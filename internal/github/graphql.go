package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
)

// Pull requests and issues come from GraphQL because one query returns what
// REST needs several calls per item for: CI status, mergeability, reviews
// and the latest comment.

const pullRequestsQuery = `query($owner: String!, $name: String!, $after: String) {
  repository(owner: $owner, name: $name) {
    items: pullRequests(states: OPEN, first: 50, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title url body isDraft createdAt updatedAt
        author { login __typename }
        labels(first: 20) { nodes { name } }
        headRefName headRefOid mergeable reviewDecision
        reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
        commits(last: 1) { nodes { commit { author { user { login } } statusCheckRollup { state } } } }
        comments(last: 1) { nodes { id databaseId body author { login __typename } } }
        reviews(last: 1) { nodes { id state body author { login __typename } comments(last: 1) { nodes { body path } } } }
      }
    }
  }
}`

const issuesQuery = `query($owner: String!, $name: String!, $after: String) {
  repository(owner: $owner, name: $name) {
    items: issues(states: OPEN, first: 50, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title url body createdAt updatedAt
        author { login __typename }
        labels(first: 20) { nodes { name } }
        comments(last: 1) { nodes { id databaseId body author { login __typename } } }
      }
    }
  }
}`

type actorJSON struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

// login matches the REST spelling, where bots end in "[bot]".
func (a *actorJSON) login() string {
	if a == nil {
		return "ghost"
	}
	if a.Typename == "Bot" && !strings.HasSuffix(a.Login, "[bot]") {
		return a.Login + "[bot]"
	}
	return a.Login
}

func (a *actorJSON) bot() bool { return a != nil && a.Typename == "Bot" }

type itemJSON struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	URL       string     `json:"url"`
	Body      string     `json:"body"`
	IsDraft   bool       `json:"isDraft"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Author    *actorJSON `json:"author"`
	Labels    struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	HeadRefName    string `json:"headRefName"`
	HeadRefOid     string `json:"headRefOid"`
	Mergeable      string `json:"mergeable"`
	ReviewDecision string `json:"reviewDecision"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Author struct {
					User *struct {
						Login string `json:"login"`
					} `json:"user"`
				} `json:"author"`
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Comments struct {
		Nodes []struct {
			ID         string     `json:"id"`
			DatabaseID int64      `json:"databaseId"`
			Body       string     `json:"body"`
			Author     *actorJSON `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			ID       string     `json:"id"`
			State    string     `json:"state"`
			Body     string     `json:"body"`
			Author   *actorJSON `json:"author"`
			Comments struct {
				Nodes []struct {
					Body string `json:"body"`
					Path string `json:"path"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviews"`
}

type pageJSON struct {
	Repository struct {
		Items struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []itemJSON `json:"nodes"`
		} `json:"items"`
	} `json:"repository"`
}

func (r *REST) fetchGraphQL(ctx context.Context, repo string, k config.EventKind) ([]Event, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("bad repository name %q", repo)
	}
	query := issuesQuery
	if k == config.KindPullRequest {
		query = pullRequestsQuery
	}
	vars := map[string]any{"owner": owner, "name": name, "after": nil}
	var out []Event
	for page := 0; page < maxPages; page++ {
		var resp pageJSON
		if err := r.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return out, err
		}
		items := resp.Repository.Items
		for _, it := range items.Nodes {
			out = append(out, it.event(repo, k))
		}
		if !items.PageInfo.HasNextPage {
			break
		}
		vars["after"] = items.PageInfo.EndCursor
	}
	return out, nil
}

func (it itemJSON) event(repo string, k config.EventKind) Event {
	e := Event{
		Key:            EventKey(repo, k, it.Number),
		Repo:           repo,
		Kind:           k,
		Number:         it.Number,
		Title:          it.Title,
		URL:            it.URL,
		Author:         it.Author.login(),
		Bot:            it.Author.bot(),
		Branch:         it.HeadRefName,
		Body:           truncate(it.Body, 4000),
		Draft:          it.IsDraft,
		CreatedAt:      it.CreatedAt,
		UpdatedAt:      it.UpdatedAt,
		HeadSHA:        it.HeadRefOid,
		Mergeable:      it.Mergeable,
		ReviewDecision: it.ReviewDecision,
	}
	for _, l := range it.Labels.Nodes {
		e.Labels = append(e.Labels, l.Name)
	}
	for _, n := range it.ReviewRequests.Nodes {
		if l := n.RequestedReviewer.Login; l != "" {
			e.ReviewRequests = append(e.ReviewRequests, l)
		}
	}
	if n := it.Commits.Nodes; len(n) > 0 {
		c := n[0].Commit
		if c.Author.User != nil {
			e.HeadAuthor = c.Author.User.Login
		}
		if c.StatusCheckRollup != nil {
			e.Checks = c.StatusCheckRollup.State
		}
	}
	if n := it.Comments.Nodes; len(n) > 0 {
		c := n[0]
		e.LastComment = Activity{ID: c.ID, DatabaseID: c.DatabaseID, Author: c.Author.login(), Bot: c.Author.bot(), Body: truncate(c.Body, 2000)}
	}
	if n := it.Reviews.Nodes; len(n) > 0 {
		rv := n[0]
		body := rv.Body
		if c := rv.Comments.Nodes; len(c) > 0 {
			body = strings.TrimSpace(body + "\n\n" + c[0].Path + ": " + c[0].Body)
		}
		e.LastReview = Activity{ID: rv.ID, Author: rv.Author.login(), Bot: rv.Author.bot(), State: rv.State, Body: truncate(body, 2000)}
	}
	return e
}
