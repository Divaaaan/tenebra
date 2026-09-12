//go:build !windows

package control

type realSystemProxy struct{}

func (realSystemProxy) Enable(target string) error    { return enableSystemProxy(target) }
func (realSystemProxy) Disable() error                { return disableSystemProxy() }
func (realSystemProxy) Get() (proxyState, error)      { return readSystemProxy() }
func newSystemProxyController() systemProxyController { return realSystemProxy{} }
func RunUserProxyHelper([]string) (bool, error)       { return false, nil }
