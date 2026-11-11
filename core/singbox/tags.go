package singbox

import (
	"fmt"
	"github.com/Divaaaan/tenebra/core/routing"
)

const maintenanceTag = "maintenance-in"

// All builders and selector projections reserve the same namespaces, even when
// an optional inbound is absent. Enabling maintenance never renames a node.
type tagAllocator struct{ used map[string]bool }

func newTagAllocator(nodeCount int) *tagAllocator {
	allocator := &tagAllocator{used: make(map[string]bool)}
	for _, name := range append(routing.ReservedTags(), tunTag, mixedTag, proxyTag, directTag, blockTag, maintenanceTag) {
		allocator.used[name] = true
	}
	for i := 0; i < nodeCount; i++ {
		allocator.used[probeInboundTag(i)] = true
	}
	return allocator
}
func (a *tagAllocator) Allocate(name string) string {
	base := sanitizeTag(name)
	if !a.used[base] {
		a.used[base] = true
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !a.used[candidate] {
			a.used[candidate] = true
			return candidate
		}
	}
}
func (a *tagAllocator) Release(name string) { delete(a.used, name) }
