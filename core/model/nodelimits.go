package model

import (
	"encoding/json"
	"fmt"
)

const (
	MaxRetainedNodes  = 2048
	MaxNodeBytes      = 16 << 10
	MaxNodeFieldBytes = 8 << 10
)

// BudgetError describes an exceeded resource limit without copying user data.
type BudgetError struct {
	Kind  string
	Limit int64
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("node limit exceeded: %s (maximum %d)", e.Kind, e.Limit)
}

// ValidateNodeRecords validates already deduplicated records before operational use.
func ValidateNodeRecords(nodes []Node) error {
	if len(nodes) > MaxRetainedNodes {
		return &BudgetError{Kind: "retained_nodes", Limit: MaxRetainedNodes}
	}
	for _, node := range nodes {
		data, err := json.Marshal(node)
		if err != nil {
			return fmt.Errorf("invalid node JSON")
		}
		if len(data) > MaxNodeBytes {
			return &BudgetError{Kind: "node_bytes", Limit: MaxNodeBytes}
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("invalid node JSON")
		}
		if nodeFieldTooLarge(value) {
			return &BudgetError{Kind: "field_bytes", Limit: MaxNodeFieldBytes}
		}
	}
	return nil
}

func nodeFieldTooLarge(value any) bool {
	switch v := value.(type) {
	case string:
		return len(v) > MaxNodeFieldBytes
	case []any:
		for _, child := range v {
			if nodeFieldTooLarge(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range v {
			if len(key) > MaxNodeFieldBytes || nodeFieldTooLarge(child) {
				return true
			}
		}
	}
	return false
}
