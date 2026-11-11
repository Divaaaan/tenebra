package profile

import (
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/singbox"
)

func ValidateIdentity(p Profile) error {
	if p.IdentityVersion != 2 {
		return ErrInvalidNodeID
	}
	seen := make(map[string]bool, len(p.Servers))
	for _, server := range p.Servers {
		if !nodeidentity.ValidID(server.ID) {
			return ErrInvalidNodeID
		}
		if seen[server.ID] {
			return ErrDuplicateNodeID
		}
		seen[server.ID] = true
	}
	return nil
}

func profileNodes(p Profile) []model.Node {
	if p.Servers == nil {
		return nil
	}
	nodes := make([]model.Node, len(p.Servers))
	for i, server := range p.Servers {
		nodes[i] = server.Node
	}
	return nodes
}

func ValidateForUse(p Profile) error {
	if err := ValidateIdentity(p); err != nil {
		return err
	}
	nodes := profileNodes(p)
	if err := model.ValidateNodeRecords(nodes); err != nil {
		return err
	}
	if len(singbox.SelectorTags(nodes)) == 0 {
		return ErrNodeUnusable
	}
	return nil
}

func NodeTags(p Profile) (map[string]string, error) {
	if err := ValidateForUse(p); err != nil {
		return nil, err
	}
	tags := make(map[string]string)
	for _, node := range singbox.SelectorTags(profileNodes(p)) {
		tags[p.Servers[node.Index].ID] = node.Tag
	}
	return tags, nil
}

func ResolveNode(p Profile, id string) (Server, string, error) {
	if err := ValidateIdentity(p); err != nil {
		return Server{}, "", err
	}
	index := -1
	for i, server := range p.Servers {
		if server.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Server{}, "", ErrNodeNotFound
	}
	tags, err := NodeTags(p)
	if err != nil {
		return Server{}, "", err
	}
	tag, ok := tags[id]
	if !ok {
		return Server{}, "", ErrNodeUnusable
	}
	return p.Servers[index], tag, nil
}

func ValidateTransition(previous, candidate Profile) error {
	if err := ValidateIdentity(previous); err != nil {
		return err
	}
	if err := ValidateIdentity(candidate); err != nil {
		return err
	}
	old := make(map[string]model.Node, len(previous.Servers))
	for _, server := range previous.Servers {
		old[server.ID] = server.Node
	}
	for _, server := range candidate.Servers {
		before, ok := old[server.ID]
		if !ok {
			continue
		}
		matcher, err := nodeidentity.NewMatcher([]model.Node{before})
		if err != nil {
			return err
		}
		matches, err := matcher.Candidates(server.Node)
		if err != nil {
			return err
		}
		if len(matches) != 1 {
			return ErrIdentityRebind
		}
	}
	return nil
}
