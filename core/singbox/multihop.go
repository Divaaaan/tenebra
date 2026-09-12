package singbox

import (
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
)

// ValidateMultihop checks the same rendered outbound capabilities as Build.
// Controllers use it before accepting settings or replacing a working tunnel.
func ValidateMultihop(nodes []model.Node, entryTag, exitTag string) error {
	outs, _, _, err := buildNodes(nodes)
	if err != nil {
		return err
	}
	return validateMultihopOutbounds(outs, entryTag, exitTag)
}

func validateMultihopOutbounds(outs []map[string]any, entryTag, exitTag string) error {
	if entryTag == "" || exitTag == "" {
		return fmt.Errorf("multihop: entry and exit nodes are required")
	}
	if entryTag == exitTag {
		return fmt.Errorf("multihop: entry and exit nodes must differ")
	}
	if _, ok := outboundByTag(outs, entryTag); !ok {
		return fmt.Errorf("multihop: entry node is missing or does not support chaining")
	}
	if _, ok := outboundByTag(outs, exitTag); !ok {
		return fmt.Errorf("multihop: exit node is missing or does not support chaining")
	}
	return nil
}
