// Package github talks to the GitHub REST API using the credentials of the
// GitHub CLI (gh).
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/drlholloway/fast-lane/internal/config"
)

// Repo is a repository the user can see.
type Repo struct {
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	Private     bool      `json:"private"`
	Fork        bool      `json:"fork"`
	Archived    bool      `json:"archived"`
	Stars       int       `json:"stargazers_count"`
	PushedAt    time.Time `json:"pushed_at"`
	HTMLURL     string    `json:"html_url"`
}

// Event is a single piece of open activity on a repository: a pull request,
// an issue, or a security alert.
type Event struct {
	Key       string
	Repo      string
	Kind      config.EventKind
	Number    int
	Title     string
	URL       string
	Author    string
	Bot       bool
	Labels    []string
	Branch    string
	Severity  string
	Body      string
	Draft     bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// Pull request state, empty for other kinds. Checks is the head commit's
	// combined status (SUCCESS, FAILURE, ERROR, PENDING) and Mergeable one of
	// MERGEABLE, CONFLICTING or UNKNOWN.
	HeadSHA        string
	HeadAuthor     string
	Checks         string
	Mergeable      string
	ReviewDecision string
	ReviewRequests []string
	// LastComment and LastReview are the newest of each, by anyone.
	LastComment Activity
	LastReview  Activity
}

// EventKey builds the stable identifier of an event.
func EventKey(repo string, kind config.EventKind, number int) string {
	return fmt.Sprintf("%s:%s:%d", strings.ToLower(repo), kind, number)
}

// Client is the subset of GitHub Fast Lane needs. It exists so the TUI can be
// exercised against a fake.
type Client interface {
	CurrentUser(ctx context.Context) (string, error)
	ListRepos(ctx context.Context) ([]Repo, error)
	ListStarred(ctx context.Context) ([]Repo, error)
	ListMyOpenPRs(ctx context.Context, login string) ([]Event, error)
	FetchEvents(ctx context.Context, repo string, kinds []config.EventKind) ([]Event, error)
	// Inbox returns review requests, mentions and assignments for me from
	// notifications updated since since.
	Inbox(ctx context.Context, me string, since time.Time) ([]InboxItem, error)
	// Auth reports who the gh credentials belong to and what they allow.
	Auth(ctx context.Context) (Auth, error)
}

// Auth describes the credentials Fast Lane uses.
type Auth struct {
	Login string
	// Scopes are the OAuth scopes of a classic token. ScopesKnown is false
	// for tokens that do not report scopes, such as fine-grained ones.
	Scopes      []string
	ScopesKnown bool
}

func (r *REST) Auth(ctx context.Context) (Auth, error) {
	resp, err := r.c.RequestWithContext(ctx, http.MethodGet, "user", nil)
	if err != nil {
		return Auth{}, err
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Auth{}, err
	}
	a := Auth{Login: u.Login}
	if h, ok := resp.Header["X-Oauth-Scopes"]; ok {
		a.ScopesKnown = true
		for _, sc := range strings.Split(strings.Join(h, ","), ",") {
			if sc = strings.TrimSpace(sc); sc != "" {
				a.Scopes = append(a.Scopes, sc)
			}
		}
	}
	return a, nil
}

// REST implements Client with go-gh, which reuses the gh CLI's auth.
type REST struct {
	c   *api.RESTClient
	gql *api.GraphQLClient
}

// NewREST returns a client authenticated as the gh CLI user.
func NewREST() (*REST, error) {
	c, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("gh auth: %w (run `gh auth login`)", err)
	}
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("gh auth: %w (run `gh auth login`)", err)
	}
	return &REST{c: c, gql: gql}, nil
}

const maxPages = 10

// getAll follows Link: rel="next" headers and decodes each page into a slice.
func getAll[T any](ctx context.Context, c *api.RESTClient, path string) ([]T, error) {
	var out []T
	next := path
	for page := 0; next != "" && page < maxPages; page++ {
		resp, err := c.RequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return out, err
		}
		var items []T
		err = json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		if err != nil {
			return out, err
		}
		out = append(out, items...)
		next = NextLink(resp.Header.Get("Link"))
	}
	return out, nil
}

var linkNextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// NextLink extracts the rel="next" URL from a Link header.
func NextLink(header string) string {
	if m := linkNextRE.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}

func (r *REST) CurrentUser(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	resp, err := r.c.RequestWithContext(ctx, http.MethodGet, "user", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (r *REST) ListRepos(ctx context.Context) ([]Repo, error) {
	return getAll[Repo](ctx, r.c, "user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member")
}

func (r *REST) ListStarred(ctx context.Context) ([]Repo, error) {
	return getAll[Repo](ctx, r.c, "user/starred?per_page=100")
}

type searchResult struct {
	Items []issueJSON `json:"items"`
}

func (r *REST) ListMyOpenPRs(ctx context.Context, login string) ([]Event, error) {
	q := url.QueryEscape(fmt.Sprintf("is:pr is:open author:%s archived:false", login))
	resp, err := r.c.RequestWithContext(ctx, http.MethodGet, "search/issues?per_page=100&q="+q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var res searchResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(res.Items))
	for _, it := range res.Items {
		repo := repoFromAPIURL(it.RepositoryURL)
		if repo == "" {
			continue
		}
		out = append(out, it.event(repo, config.KindPullRequest))
	}
	return out, nil
}

// FetchEvents returns all open activity of the requested kinds. Alert kinds
// the user cannot read (feature disabled, no permission) are skipped silently.
func (r *REST) FetchEvents(ctx context.Context, repo string, kinds []config.EventKind) ([]Event, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  []Event
		errs []error
	)
	for _, k := range kinds {
		wg.Add(1)
		go func(k config.EventKind) {
			defer wg.Done()
			evs, err := r.fetchKind(ctx, repo, k)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && !(isAlertKind(k) && isUnavailable(err)) {
				errs = append(errs, fmt.Errorf("%s %s: %w", repo, k.Short(), err))
			}
			out = append(out, evs...)
		}(k)
	}
	wg.Wait()
	SortEvents(out)
	return out, errors.Join(errs...)
}

func (r *REST) fetchKind(ctx context.Context, repo string, k config.EventKind) ([]Event, error) {
	base := "repos/" + repo
	switch k {
	case config.KindPullRequest, config.KindIssue:
		return r.fetchGraphQL(ctx, repo, k)
	case config.KindDependabot:
		items, err := getAll[dependabotJSON](ctx, r.c, base+"/dependabot/alerts?state=open&per_page=100")
		out := make([]Event, 0, len(items))
		for _, a := range items {
			out = append(out, a.event(repo))
		}
		return out, err
	case config.KindCodeScanning:
		items, err := getAll[codeScanJSON](ctx, r.c, base+"/code-scanning/alerts?state=open&per_page=100")
		out := make([]Event, 0, len(items))
		for _, a := range items {
			out = append(out, a.event(repo))
		}
		return out, err
	}
	return nil, fmt.Errorf("unknown event kind %q", k)
}

func isAlertKind(k config.EventKind) bool {
	return k == config.KindDependabot || k == config.KindCodeScanning
}

func isUnavailable(err error) bool {
	var he *api.HTTPError
	if errors.As(err, &he) {
		return he.StatusCode == http.StatusForbidden || he.StatusCode == http.StatusNotFound
	}
	return false
}

// SortEvents orders events most recently updated first.
func SortEvents(evs []Event) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].UpdatedAt.After(evs[j].UpdatedAt) })
}

type userJSON struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type issueJSON struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	HTMLURL string   `json:"html_url"`
	Body    string   `json:"body"`
	User    userJSON `json:"user"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Draft         bool      `json:"draft"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	RepositoryURL string    `json:"repository_url"`
	PullRequest   *struct{} `json:"pull_request"`
	Head          *struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	State              string     `json:"state"`
	Assignees          []userJSON `json:"assignees"`
	RequestedReviewers []userJSON `json:"requested_reviewers"`
	RequestedTeams     []struct{} `json:"requested_teams"`
}

func (it issueJSON) event(repo string, k config.EventKind) Event {
	e := Event{
		Key:       EventKey(repo, k, it.Number),
		Repo:      repo,
		Kind:      k,
		Number:    it.Number,
		Title:     it.Title,
		URL:       it.HTMLURL,
		Author:    it.User.Login,
		Bot:       it.User.Type == "Bot",
		Body:      truncate(it.Body, 4000),
		Draft:     it.Draft,
		CreatedAt: it.CreatedAt,
		UpdatedAt: it.UpdatedAt,
	}
	if it.Head != nil {
		e.Branch, e.HeadSHA = it.Head.Ref, it.Head.SHA
	}
	for _, u := range it.RequestedReviewers {
		e.ReviewRequests = append(e.ReviewRequests, u.Login)
	}
	for _, l := range it.Labels {
		e.Labels = append(e.Labels, l.Name)
	}
	return e
}

type dependabotJSON struct {
	Number     int       `json:"number"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Dependency struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		ManifestPath string `json:"manifest_path"`
	} `json:"dependency"`
	SecurityAdvisory struct {
		GHSAID   string `json:"ghsa_id"`
		Summary  string `json:"summary"`
		Severity string `json:"severity"`
	} `json:"security_advisory"`
	SecurityVulnerability struct {
		VulnerableVersionRange string `json:"vulnerable_version_range"`
		FirstPatchedVersion    *struct {
			Identifier string `json:"identifier"`
		} `json:"first_patched_version"`
	} `json:"security_vulnerability"`
}

func (a dependabotJSON) event(repo string) Event {
	pkg := a.Dependency.Package
	patched := "none"
	if p := a.SecurityVulnerability.FirstPatchedVersion; p != nil && p.Identifier != "" {
		patched = p.Identifier
	}
	body := fmt.Sprintf("Advisory %s: %s\nPackage: %s (%s) in %s\nVulnerable range: %s\nFirst patched version: %s",
		a.SecurityAdvisory.GHSAID, a.SecurityAdvisory.Summary, pkg.Name, pkg.Ecosystem,
		a.Dependency.ManifestPath, a.SecurityVulnerability.VulnerableVersionRange, patched)
	return Event{
		Key:       EventKey(repo, config.KindDependabot, a.Number),
		Repo:      repo,
		Kind:      config.KindDependabot,
		Number:    a.Number,
		Title:     fmt.Sprintf("%s: %s", pkg.Name, a.SecurityAdvisory.Summary),
		URL:       a.HTMLURL,
		Author:    "dependabot",
		Bot:       true,
		Severity:  a.SecurityAdvisory.Severity,
		Body:      body,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

type codeScanJSON struct {
	Number    int       `json:"number"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Rule      struct {
		ID                    string `json:"id"`
		Severity              string `json:"severity"`
		SecuritySeverityLevel string `json:"security_severity_level"`
		Description           string `json:"description"`
	} `json:"rule"`
	Tool struct {
		Name string `json:"name"`
	} `json:"tool"`
	MostRecentInstance struct {
		Ref      string `json:"ref"`
		Location struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
		} `json:"location"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
	} `json:"most_recent_instance"`
}

func (a codeScanJSON) event(repo string) Event {
	sev := a.Rule.SecuritySeverityLevel
	if sev == "" {
		sev = a.Rule.Severity
	}
	inst := a.MostRecentInstance
	body := fmt.Sprintf("Tool: %s\nRule: %s (%s)\nLocation: %s:%d on %s\nMessage: %s",
		a.Tool.Name, a.Rule.ID, a.Rule.Description, inst.Location.Path, inst.Location.StartLine,
		inst.Ref, inst.Message.Text)
	return Event{
		Key:       EventKey(repo, config.KindCodeScanning, a.Number),
		Repo:      repo,
		Kind:      config.KindCodeScanning,
		Number:    a.Number,
		Title:     a.Rule.Description,
		URL:       a.HTMLURL,
		Author:    a.Tool.Name,
		Severity:  sev,
		Body:      body,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

// repoFromAPIURL turns https://api.github.com/repos/o/r into "o/r".
func repoFromAPIURL(u string) string {
	i := strings.Index(u, "/repos/")
	if i < 0 {
		return ""
	}
	return u[i+len("/repos/"):]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}
