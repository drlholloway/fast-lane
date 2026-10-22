package github

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Release is a published GitHub release.
type Release struct {
	Tag       string
	URL       string
	Published time.Time
}

// LatestRelease returns repo's newest release that isn't a draft or a
// prerelease, or a zero Release when it has none.
func (r *REST) LatestRelease(ctx context.Context, repo string) (Release, error) {
	var rel struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := r.getJSON(ctx, "repos/"+repo+"/releases/latest", &rel); err != nil {
		var he *api.HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
			return Release{}, nil
		}
		return Release{}, err
	}
	return Release{Tag: rel.TagName, URL: rel.HTMLURL, Published: rel.PublishedAt}, nil
}
