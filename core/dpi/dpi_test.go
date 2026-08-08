package dpi

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBinaryNameSuffix(t *testing.T) {
	name := BinaryName()
	if runtime.GOOS == "windows" {
		if name != "ciadpi.exe" {
			t.Errorf("windows binary = %q, want ciadpi.exe", name)
		}
	} else if name != "ciadpi" {
		t.Errorf("non-windows binary = %q, want ciadpi", name)
	}
}

func TestResolveBinaryEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-ciadpi")
	t.Setenv(BinaryEnv, want)

	got, err := ResolveBinary()
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if got != want {
		t.Errorf("override path = %q, want %q", got, want)
	}
}

func TestResolveBinaryNextToExe(t *testing.T) {
	t.Setenv(BinaryEnv, "")

	got, err := ResolveBinary()
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), BinaryName())
	if got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
}

func TestFreePortIsUsable(t *testing.T) {
	port := FreePort()
	if port < 1 || port > 65535 {
		t.Fatalf("FreePort = %d, out of range", port)
	}
	// The picked port must be bindable on loopback: that is the whole promise,
	// and it is what the generated config will point the bypass outbound at.
	l, err := net.Listen("tcp", net.JoinHostPort(LoopbackHost, fmt.Sprint(port)))
	if err != nil {
		t.Fatalf("listening on the port FreePort handed out (%d): %v", port, err)
	}
	l.Close()
}

func TestFreePortVaries(t *testing.T) {
	// Two consecutive picks should differ: the kernel hands out a fresh
	// ephemeral port each time. Equal values would mean the fallback path is
	// being taken, which only happens when loopback cannot be bound at all.
	if a, b := FreePort(), FreePort(); a == b && a == DefaultPort {
		t.Errorf("FreePort fell back to the default twice (%d); loopback listen is failing", a)
	}
}

func TestDefaultPortMatchesConfigContract(t *testing.T) {
	// The generated sing-box config points its "dpi" outbound at this port
	// (singbox.DefaultDPIPort). The constant is duplicated because the import
	// runs the other way; this test is the reminder that the two are one number.
	if DefaultPort != 2081 {
		t.Errorf("DefaultPort = %d, want 2081 to match singbox.DefaultDPIPort", DefaultPort)
	}
}
