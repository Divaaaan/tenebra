package subscription

import (
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
)

type Limits struct {
	BodyBytes                                        int64
	RawEntries, RetainedNodes, NodeBytes, FieldBytes int
}

type BudgetError = model.BudgetError

func DefaultLimits() Limits { return Limits{8 << 20, 4096, 2048, 16 << 10, 8 << 10} }

func (l Limits) valid() bool {
	d := DefaultLimits()
	return l.BodyBytes > 0 && l.BodyBytes <= d.BodyBytes && l.RawEntries > 0 && l.RawEntries <= d.RawEntries &&
		l.RetainedNodes > 0 && l.RetainedNodes <= d.RetainedNodes && l.NodeBytes > 0 && l.NodeBytes <= d.NodeBytes && l.FieldBytes > 0 && l.FieldBytes <= d.FieldBytes
}

var errInvalidLimits = errors.New("invalid subscription limits")

type parseBudget struct {
	limits  Limits
	entries int
}

func (b *parseBudget) takeEntry() error {
	if b.entries >= b.limits.RawEntries {
		return &BudgetError{Kind: "raw_entries", Limit: int64(b.limits.RawEntries)}
	}
	b.entries++
	return nil
}

func (b *parseBudget) rawRecord(size int) error {
	if size > 4*b.limits.NodeBytes {
		return &BudgetError{Kind: "raw_node_bytes", Limit: int64(4 * b.limits.NodeBytes)}
	}
	return nil
}

func validateRecord(node model.Node, limits Limits) error {
	data, err := json.Marshal(node)
	if err != nil {
		return errors.New("invalid normalized node JSON")
	}
	if len(data) > limits.NodeBytes {
		return &BudgetError{Kind: "node_bytes", Limit: int64(limits.NodeBytes)}
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return errors.New("invalid normalized node JSON")
	}
	if fieldExceeds(value, limits.FieldBytes) {
		return &BudgetError{Kind: "field_bytes", Limit: int64(limits.FieldBytes)}
	}
	return nil
}

func fieldExceeds(value any, max int) bool {
	switch v := value.(type) {
	case string:
		return len(v) > max
	case []any:
		for _, child := range v {
			if fieldExceeds(child, max) {
				return true
			}
		}
	case map[string]any:
		for key, child := range v {
			if len(key) > max || fieldExceeds(child, max) {
				return true
			}
		}
	}
	return false
}

func ValidateNodes(nodes []model.Node, limits Limits) ([]model.Node, error) {
	if !limits.valid() {
		return nil, errInvalidLimits
	}
	if len(nodes) > limits.RawEntries {
		return nil, &BudgetError{Kind: "raw_entries", Limit: int64(limits.RawEntries)}
	}
	for _, node := range nodes {
		if err := validateRecord(node, limits); err != nil {
			return nil, err
		}
	}
	result, err := nodeidentity.DeduplicateRecords(nodes)
	if err != nil {
		return nil, err
	}
	if len(result) > limits.RetainedNodes {
		return nil, &BudgetError{Kind: "retained_nodes", Limit: int64(limits.RetainedNodes)}
	}
	return result, nil
}
