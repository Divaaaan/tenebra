//go:build windows

package control

import (
	"testing"
	"unsafe"
)

func TestUserProxyWinINetABILayout(t *testing.T) {
	var option internetPerConnOption
	var list internetPerConnList
	if unsafe.Sizeof(uintptr(0)) == 8 {
		if unsafe.Sizeof(option) != 16 || unsafe.Offsetof(option.Value) != 8 || unsafe.Sizeof(list) != 32 || unsafe.Offsetof(list.Options) != 24 {
			t.Fatal("WinINet 64-bit ABI mismatch")
		}
	} else if unsafe.Sizeof(option) != 12 || unsafe.Offsetof(option.Value) != 4 || unsafe.Sizeof(list) != 20 || unsafe.Offsetof(list.Options) != 16 {
		t.Fatal("WinINet 32-bit ABI mismatch")
	}
}

func TestUserProxyHelperRejectsInvalidArgumentsBeforeNativeWork(t *testing.T) {
	for _, args := range [][]string{{proxyHelperFlag}, {proxyHelperFlag, "restore", "extra"}, {proxyHelperFlag, "apply", "192.0.2.1:80"}, {proxyHelperFlag, "anything"}} {
		if handled, err := RunUserProxyHelper(args); !handled || err == nil {
			t.Fatalf("accepted malformed helper arguments: %v", args)
		}
	}
	if handled, err := RunUserProxyHelper([]string{"--pipe"}); handled || err != nil {
		t.Fatal("ordinary core flags treated as proxy helper")
	}
}

// TestFirstProxyTarget pins how a WinINet ProxyServer value is reduced to a bare
// host:port for the startup reconcile's comparison: a plain value passes through,
// and a per-protocol list yields its first target with the "scheme=" prefix
// stripped. The reconcile compares this against our own loopback address, so a
// wrong reduction here would either miss a stale proxy or (worse) match a foreign
// one.
func TestFirstProxyTarget(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain host:port", "127.0.0.1:2080", "127.0.0.1:2080"},
		{"surrounding whitespace", "  127.0.0.1:2080  ", "127.0.0.1:2080"},
		{"per-protocol list", "http=127.0.0.1:2080;https=127.0.0.1:2080", "127.0.0.1:2080"},
		{"single scheme prefix", "http=127.0.0.1:2080", "127.0.0.1:2080"},
		{"remote proxy list", "http=10.0.0.1:8080;https=10.0.0.1:8080", "10.0.0.1:8080"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstProxyTarget(c.in); got != c.want {
				t.Errorf("firstProxyTarget(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
