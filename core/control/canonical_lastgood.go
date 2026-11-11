package control

import (
	"context"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
)

const maxPendingLastGood = 32

// The hint is optional: lifecycle callers enqueue bounded metadata only. Its
// independent worker enters the repository without connMu or protectionOp.
type canonicalLastGood struct {
	daemon  *Daemon
	mu      sync.Mutex
	pending map[string]string
	wake    chan struct{}
}

func newCanonicalLastGood(d *Daemon) *canonicalLastGood {
	return &canonicalLastGood{daemon: d, pending: make(map[string]string), wake: make(chan struct{}, 1)}
}

func (c *canonicalLastGood) start() {
	c.daemon.entWG.Add(1)
	go func() {
		defer c.daemon.entWG.Done()
		ctx := c.daemon.entCtx
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
			}
			for ctx.Err() == nil {
				c.mu.Lock()
				var profileID, nodeID string
				for p, n := range c.pending {
					profileID, nodeID = p, n
					delete(c.pending, p)
					break
				}
				c.mu.Unlock()
				if profileID == "" {
					break
				}
				writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				c.persist(writeCtx, profileID, nodeID)
				cancel()
			}
		}
	}()
}

func (c *canonicalLastGood) Get(profileID string) (string, bool) {
	ref, ok := c.daemon.canonical.View().LastGood[profileID]
	return ref.NodeID, ok && ref.Resolution == state.Resolved
}
func (c *canonicalLastGood) Set(profileID, nodeID string) {
	if profileID == "" || len(profileID) > 128 || nodeID == "" || len(nodeID) > 128 || c.daemon.entCtx.Err() != nil {
		return
	}
	c.mu.Lock()
	_, exists := c.pending[profileID]
	if !exists && len(c.pending) >= maxPendingLastGood {
		c.mu.Unlock()
		return
	}
	c.pending[profileID] = nodeID
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *canonicalLastGood) persist(ctx context.Context, profileID, nodeID string) {
	_, err := c.daemon.CommitCanonical(ctx, state.AnyRevision, func(s *state.Snapshot) error {
		p, ok := s.Profiles[profileID]
		if !ok {
			return state.ErrProfileConflict
		}
		if _, _, err := profile.ResolveNode(p, nodeID); err != nil {
			return err
		}
		s.LastGood[profileID] = state.NodeRef{ProfileID: profileID, NodeID: nodeID, Resolution: state.Resolved}
		return nil
	})
	if err != nil && c.daemon.entCtx.Err() == nil {
		c.daemon.emitLog(LogWarn, "last-good persistence failed")
	}
}
