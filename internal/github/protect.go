package github

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

// The rulesets Protect adds. GitHub enforces them for everyone, Fast Lane
// and people alike; a repository admin can change or remove them in the
// repository's settings, so only a human can undo them.
var historyRulesets = []map[string]any{
	{
		"name":        "Fast Lane: protect the default branch",
		"target":      "branch",
		"enforcement": "active",
		"conditions":  map[string]any{"ref_name": map[string]any{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}},
		"rules":       []map[string]string{{"type": "deletion"}, {"type": "non_fast_forward"}},
	},
	{
		"name":        "Fast Lane: protect tags",
		"target":      "tag",
		"enforcement": "active",
		"conditions":  map[string]any{"ref_name": map[string]any{"include": []string{"~ALL"}, "exclude": []string{}}},
		"rules":       []map[string]string{{"type": "deletion"}, {"type": "non_fast_forward"}, {"type": "update"}},
	},
}

// Protect adds the history rulesets repo doesn't have yet.
func (r *REST) Protect(ctx context.Context, repo string) ([]string, error) {
	var have []struct {
		Name string `json:"name"`
	}
	if err := r.getJSON(ctx, "repos/"+repo+"/rulesets", &have); err != nil {
		return nil, err
	}
	var added []string
	for _, rs := range historyRulesets {
		name := rs["name"].(string)
		if slices.ContainsFunc(have, func(h struct {
			Name string `json:"name"`
		}) bool {
			return h.Name == name
		}) {
			continue
		}
		body, _ := json.Marshal(rs)
		resp, err := r.c.RequestWithContext(ctx, http.MethodPost, "repos/"+repo+"/rulesets", bytes.NewReader(body))
		if err != nil {
			return added, err
		}
		resp.Body.Close()
		added = append(added, name)
	}
	return added, nil
}
