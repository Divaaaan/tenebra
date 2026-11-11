package installtx

import (
	"context"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type rootReadyProbe struct {
	root    *securefs.Root
	factory func(*securefs.Root, string) (ReadyProbe, error)
}

// The installer generates its transaction inside Apply/Recover. Its probe must
// route that exact verified expectation to the corresponding private endpoint,
// rather than being prebound to an unrelated transaction at construction.
func NewMaintenanceReadyProbe(root *securefs.Root) (ReadyProbe, error) {
	if root == nil {
		return nil, ErrProtocol
	}
	if _, err := root.Path(); err != nil {
		return nil, err
	}
	return &rootReadyProbe{root: root, factory: NewReadyProbe}, nil
}
func (p *rootReadyProbe) Exchange(ctx context.Context, process ProcessIdentity, request ReadyRequest) (ReadyReply, error) {
	if p == nil || p.root == nil || p.factory == nil || !validHostIdentity(process) ||
		(PrivateRequest{Schema: request.Schema, Cmd: request.Cmd, TxID: request.TxID, Challenge: request.Challenge}).validate() != nil || request.Cmd != "ready" {
		return ReadyReply{}, ErrReadiness
	}
	if err := ctx.Err(); err != nil {
		return ReadyReply{}, err
	}
	probe, err := p.factory(p.root, request.TxID)
	if err != nil {
		return ReadyReply{}, err
	}
	return probe.Exchange(ctx, process, request)
}
