package nodeidentity

import "github.com/Divaaaan/tenebra/core/model"

func DeduplicateRecords(nodes []model.Node) ([]model.Node, error) {
	if nodes == nil {
		return nil, nil
	}
	result := make([]model.Node, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		key, err := canonicalRecord(node, true)
		if err != nil {
			return nil, err
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, node)
		}
	}
	return result, nil
}
