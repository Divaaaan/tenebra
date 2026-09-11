package control

// proxyUser identifies the owner of a per-user proxy lease. Session ID alone
// can be reused after logout; SID must also match before any cleanup is run.
type proxyUser struct {
	SID     string
	Session uint32
}

type userProxySessionOps interface {
	Current() (proxyUser, error)
	Run(proxyUser, string, string) error
	Read(proxyUser) (proxyState, error)
	HasLease(proxyUser) (bool, error)
}

type sessionSystemProxy struct {
	ops   userProxySessionOps
	owner *proxyUser
}

func (p *sessionSystemProxy) Enable(target string) error {
	if p.owner == nil {
		u, err := p.ops.Current()
		if err != nil {
			return err
		}
		p.owner = &u // retain the user even if apply fails after a partial write
	}
	return p.ops.Run(*p.owner, "apply", target)
}

func (p *sessionSystemProxy) Disable() error {
	if p.owner == nil {
		return nil
	}
	if err := p.ops.Run(*p.owner, "restore", ""); err != nil {
		return err
	}
	p.owner = nil
	return nil
}

func (p *sessionSystemProxy) Get() (proxyState, error) {
	u, err := p.ops.Current()
	if err != nil {
		return proxyState{}, err
	}
	return p.ops.Read(u)
}

// Reconcile restores only a durable Tenebra lease, including a partially
// applied or already-disabled proxy. Merely sharing our port is not ownership.
func (p *sessionSystemProxy) Reconcile() (bool, error) {
	if p.owner != nil {
		return true, p.Disable()
	}
	u, err := p.ops.Current()
	if err != nil {
		return false, err
	}
	has, err := p.ops.HasLease(u)
	if err != nil || !has {
		return false, err
	}
	p.owner = &u
	return true, p.Disable()
}
