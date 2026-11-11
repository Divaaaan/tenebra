package profile

import (
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
)

var (
	ErrRequiredNodeLost = errors.New("required node would be lost")
	ErrDuplicateNodeID  = errors.New("duplicate node identity")
	ErrInvalidNodeID    = errors.New("invalid opaque node identity")
	ErrIdentityRebind   = errors.New("opaque identity cannot be rebound to a different connection")
	ErrNodeNotFound     = errors.New("selected node not found")
	ErrNodeUnusable     = errors.New("selected node is unusable")
)

func ReconcileServers(previous []Server, nodes []model.Node, required []string) ([]Server, error) {
	return reconcileServers(previous, nodes, required, nodeidentity.NewID)
}

// The allocator seam is per invocation; tests cannot mutate a global RNG.
func reconcileServers(previous []Server, nodes []model.Node, required []string, nextID func() (string, error)) ([]Server, error) {
	nodes, err := nodeidentity.DeduplicateRecords(nodes)
	if err != nil {
		return nil, err
	}
	if err := model.ValidateNodeRecords(nodes); err != nil {
		return nil, err
	}
	// Do not retain pointers/maps owned by the import proposal's caller.
	if nodes != nil {
		data, err := json.Marshal(nodes)
		if err != nil {
			return nil, errInvalidJSON
		}
		if json.Unmarshal(data, &nodes) != nil {
			return nil, errInvalidJSON
		}
	}
	matcher, err := nodeidentity.NewMatcher(nodes)
	if err != nil {
		return nil, err
	}
	allIDs := make(map[string]bool, len(previous)+len(nodes))
	byID := make(map[string]int, len(previous))
	for i, server := range previous {
		if !nodeidentity.ValidID(server.ID) {
			return nil, ErrInvalidNodeID
		}
		if allIDs[server.ID] {
			return nil, ErrDuplicateNodeID
		}
		allIDs[server.ID] = true
		byID[server.ID] = i
	}
	if nodes == nil {
		if len(required) > 0 {
			return nil, ErrRequiredNodeLost
		}
		return nil, nil
	}
	result := make([]Server, len(nodes))
	used := make([]bool, len(nodes))
	matched := make([]bool, len(previous))
	claim := func(index int, labelOnly bool) (bool, error) {
		candidates, err := matcher.Candidates(previous[index].Node)
		if err != nil {
			return false, err
		}
		chosen := -1
		for _, candidate := range candidates {
			if !used[candidate] && nodes[candidate].Name == previous[index].Name {
				chosen = candidate
				break
			}
		}
		if chosen < 0 && !labelOnly {
			for _, candidate := range candidates {
				if !used[candidate] {
					chosen = candidate
					break
				}
			}
		}
		if chosen < 0 {
			return false, nil
		}
		used[chosen] = true
		matched[index] = true
		result[chosen] = Server{ID: previous[index].ID, Node: nodes[chosen]}
		return true, nil
	}
	for _, id := range required {
		index, ok := byID[id]
		if !ok {
			return nil, ErrRequiredNodeLost
		}
		if matched[index] {
			continue
		}
		ok, err := claim(index, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrRequiredNodeLost
		}
	}
	// Preserve every possible exact label before using previous-order matches.
	for _, labelOnly := range []bool{true, false} {
		for index := range previous {
			if matched[index] {
				continue
			}
			if _, err := claim(index, labelOnly); err != nil {
				return nil, err
			}
		}
	}
	for index, node := range nodes {
		if used[index] {
			continue
		}
		id, err := nextID()
		if err != nil {
			return nil, err
		}
		if !nodeidentity.ValidID(id) {
			return nil, ErrInvalidNodeID
		}
		if allIDs[id] {
			return nil, ErrDuplicateNodeID
		}
		allIDs[id] = true
		result[index] = Server{ID: id, Node: node}
	}
	return result, nil
}
