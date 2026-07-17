package tenebracore

import (
	"encoding/json"
	"fmt"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// orderRequest is the JSON envelope orderNodes accepts.
type orderRequest struct {
	// Profile is the profile whose nodes to order (as returned by
	// ImportSubscription and stored by the app). Only its ID and servers are read.
	Profile profile.Profile `json:"profile"`
	// LastGood is the stable server ID that most recently connected, if any. It
	// leads the protocol-preference order so the walk retries what worked first.
	LastGood string `json:"lastGood,omitempty"`
	// Latency maps a stable server ID to its measured round-trip in milliseconds.
	// When non-empty, nodes are ordered fastest-first instead of by protocol
	// preference; a server absent here (or non-positive) sorts last but is still
	// tried, so the anti-DPI fallback is preserved either way.
	Latency map[string]int64 `json:"latency,omitempty"`
}

// orderNodes returns the profile's selector tags in the order the native connect
// loop should try them, so it doesn't have to re-implement the anti-DPI fallback
// walk. The order is the fallback package's: last-good first (or fastest-first when
// latency is supplied), then protocol preference, with every node tried exactly
// once. The output is a JSON array of sing-box selector tags — the same tags
// GenerateConfig emits — which the app feeds to libbox's SelectOutbound. An empty
// or all-unusable profile yields an empty array (a deterministic default), never
// an error.
func orderNodes(requestJSON string) (string, error) {
	var req orderRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("tenebracore: decode order request: %w", err)
	}

	nodes := make([]model.Node, len(req.Profile.Servers))
	for i, s := range req.Profile.Servers {
		nodes[i] = s.Node
	}

	// The selector speaks tags; the fallback walk and the caller's signals speak
	// stable server IDs. SelectorTags gives the builder's own node->tag mapping —
	// dropping exactly the nodes GenerateConfig drops — which we pair with each
	// server's ID so the walk runs on IDs and the result comes back out as tags.
	sel := singbox.SelectorTags(nodes)
	candidates := make([]fallback.Attempt, 0, len(sel))
	tagOfID := make(map[string]string, len(sel))
	for _, nt := range sel {
		srv := req.Profile.Servers[nt.Index]
		candidates = append(candidates, fallback.Attempt{NodeID: srv.ID, Node: srv.Node})
		tagOfID[srv.ID] = nt.Tag
	}

	var machine *fallback.Machine
	if len(req.Latency) > 0 {
		machine = fallback.NewByLatency(req.Profile.ID, candidates, req.Latency, seedLastGood(req.Profile.ID, req.LastGood))
	} else {
		machine = fallback.New(req.Profile.ID, candidates, nil, seedLastGood(req.Profile.ID, req.LastGood))
	}

	ordered := machine.Order()
	tags := make([]string, 0, len(ordered))
	for _, a := range ordered {
		tags = append(tags, tagOfID[a.NodeID])
	}

	out, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("tenebracore: encode order: %w", err)
	}
	return string(out), nil
}

// seedLastGood returns an in-memory LastGood pre-set with nodeID for profileID so
// the fallback walk leads with the node the caller reports last worked, or nil when
// none was supplied.
func seedLastGood(profileID, nodeID string) fallback.LastGood {
	if nodeID == "" {
		return nil
	}
	lg := fallback.NewMemLastGood()
	lg.Set(profileID, nodeID)
	return lg
}
