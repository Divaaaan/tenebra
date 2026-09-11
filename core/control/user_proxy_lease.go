package control

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// userProxySettings captures the per-connection WinINet settings, including PAC
// and autodetection flags. It belongs to the interactive user, never LocalSystem.
type userProxySettings struct {
	Flags  uint32 `json:"flags"`
	Server string `json:"server"`
	Bypass string `json:"bypass"`
	PAC    string `json:"pac"`
}

type userProxyLease struct {
	Version int               `json:"version"`
	Before  userProxySettings `json:"before"`
	Applied userProxySettings `json:"applied"`
}

type userProxyOperations interface {
	Read() (userProxySettings, error)
	Write(userProxySettings) error
	Load() (*userProxyLease, error)
	Save(userProxyLease) error
	Delete() error
}

func validUserProxyTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	p, err := strconv.Atoi(port)
	return ip != nil && ip.IsLoopback() && err == nil && p > 0 && p <= 65535
}

func applyUserProxy(o userProxyOperations, target string) error {
	if !validUserProxyTarget(target) {
		return errors.New("system proxy target must be a loopback IP and valid port")
	}
	lease, err := o.Load()
	if err != nil {
		return fmt.Errorf("load system proxy snapshot: %w", err)
	}
	if lease != nil {
		current, err := o.Read()
		if err != nil {
			return err
		}
		if lease.Version == 1 && lease.Applied.Server == target && current == lease.Applied {
			return nil
		}
		if err := restoreUserProxy(o); err != nil {
			return err
		}
	}
	before, err := o.Read()
	if err != nil {
		return fmt.Errorf("read user proxy: %w", err)
	}
	want := userProxySettings{Flags: 3, Server: target, Bypass: "localhost;127.0.0.1;[::1]"}
	if err := o.Save(userProxyLease{Version: 1, Before: before, Applied: want}); err != nil {
		return fmt.Errorf("save user proxy rollback snapshot: %w", err)
	}
	if err := o.Write(want); err != nil {
		return errors.Join(fmt.Errorf("apply user proxy: %w", err), restoreUserProxy(o))
	}
	got, err := o.Read()
	if err != nil || got != want {
		if err == nil {
			err = errors.New("user proxy settings did not take effect")
		}
		return errors.Join(err, restoreUserProxy(o))
	}
	return nil
}

func restoreUserProxy(o userProxyOperations) error {
	lease, err := o.Load()
	if err != nil {
		return fmt.Errorf("load user proxy rollback snapshot: %w", err)
	}
	if lease == nil {
		return nil
	}
	if lease.Version != 1 || !validUserProxyTarget(lease.Applied.Server) {
		return errors.New("invalid user proxy ownership record; automatic restore refused")
	}
	current, err := o.Read()
	if err != nil {
		return err
	}
	// A different server is an explicit subsequent user/tool change. Do not
	// restore old flags/PAC over that newer configuration.
	if current.Server != lease.Applied.Server && current.Server != lease.Before.Server {
		return o.Delete()
	}
	want := current
	// Restore only fields still equal to our write. This also rolls back a
	// partially completed option list without clobbering independent edits.
	if current.Flags == lease.Applied.Flags {
		want.Flags = lease.Before.Flags
	}
	if current.Server == lease.Applied.Server {
		want.Server = lease.Before.Server
	}
	if current.Bypass == lease.Applied.Bypass {
		want.Bypass = lease.Before.Bypass
	}
	if current.PAC == lease.Applied.PAC {
		want.PAC = lease.Before.PAC
	}
	if want != current {
		if err := o.Write(want); err != nil {
			return fmt.Errorf("restore user proxy: %w", err)
		}
		got, err := o.Read()
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("user proxy restore did not take effect")
		}
	}
	return o.Delete()
}
