package tenebracore

import (
	"encoding/json"
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// tagsRequest is the JSON envelope nodeTags accepts.
type tagsRequest struct {
	// Profile is the profile whose node->tag mapping to return (as returned by
	// ImportSubscription and stored by the app). Only its servers are read.
	Profile profile.Profile `json:"profile"`
}

// NodeTags returns the map from a profile's stable server ID to the sing-box
// selector tag GenerateConfig assigns that node's outbound. The app uses it to
// switch the live tunnel to a specific node without a rebuild: pair the tapped
// node's server ID with its tag here, then hand the tag to the running selector
// (clash-api PUT /proxies or libbox SelectOutbound). Nodes GenerateConfig drops
// (unknown protocol, invalid) are absent here, exactly as they are absent from the
// selector — so a tag returned is always one the running config actually has. An
// empty or all-unusable profile yields an empty object ("{}"), never an error.
func NodeTags(requestJSON string) (string, error) {
	var req tagsRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("tenebracore: decode tags request: %w", err)
	}

	nodes := make([]model.Node, len(req.Profile.Servers))
	for i, s := range req.Profile.Servers {
		nodes[i] = s.Node
	}

	// SelectorTags gives the builder's own node->tag mapping, dropping exactly the
	// nodes GenerateConfig drops. Pair each surviving tag with its server ID.
	tagOfID := make(map[string]string)
	for _, nt := range singbox.SelectorTags(nodes) {
		tagOfID[req.Profile.Servers[nt.Index].ID] = nt.Tag
	}

	out, err := json.Marshal(tagOfID)
	if err != nil {
		return "", fmt.Errorf("tenebracore: encode tags: %w", err)
	}
	return string(out), nil
}
