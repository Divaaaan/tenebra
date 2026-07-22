package tenebracore

import (
	"encoding/json"
	"testing"
)

func TestNodeTagsMapsIDToSelectorTag(t *testing.T) {
	// Two distinct names produce two distinct sanitized tags; each server's stable
	// ID must map to its own outbound tag (the same tags OrderNodes/GenerateConfig
	// emit).
	p := bridgeProfile(t, vlessNode("alpha"), hy2Node("beta"))
	reqJSON, _ := json.Marshal(tagsRequest{Profile: p})

	out, err := NodeTags(string(reqJSON))
	if err != nil {
		t.Fatalf("NodeTags() error = %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not a JSON object: %v", err)
	}
	if got := m[p.Servers[0].ID]; got != "alpha" {
		t.Errorf("tag for server 0 = %q, want %q", got, "alpha")
	}
	if got := m[p.Servers[1].ID]; got != "beta" {
		t.Errorf("tag for server 1 = %q, want %q", got, "beta")
	}
	if len(m) != 2 {
		t.Errorf("got %d entries, want 2", len(m))
	}
}

func TestNodeTagsEmptyProfile(t *testing.T) {
	// A profile with no usable nodes yields an empty object, not null and not an
	// error — a deterministic default the app can iterate over safely.
	empty, _ := json.Marshal(tagsRequest{Profile: bridgeProfile(t)})
	out, err := NodeTags(string(empty))
	if err != nil {
		t.Fatalf("NodeTags() error = %v", err)
	}
	if out != "{}" {
		t.Errorf("empty profile tags = %s, want {}", out)
	}
}
