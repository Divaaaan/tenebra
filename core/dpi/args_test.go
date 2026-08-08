package dpi

import (
	"strings"
	"testing"
)

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildArgsRendersListenerThenStrategy(t *testing.T) {
	got, err := BuildArgs(LoopbackHost, 10800, DefaultStrategies[0].Args)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{
		"-i", "127.0.0.1", "-p", "10800",
		"--split", "1", "--disorder", "3+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s",
	}
	if !equalArgs(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestBuildArgsWithoutStrategy(t *testing.T) {
	got, err := BuildArgs(LoopbackHost, DefaultPort, nil)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{"-i", "127.0.0.1", "-p", "2081"}
	if !equalArgs(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestBuildArgsRejectsNonLoopbackListener(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
	}{
		{"any address", "0.0.0.0", 2081},
		{"lan address", "192.168.1.10", 2081},
		{"hostname", "localhost", 2081},
		{"empty", "", 2081},
		{"port zero", LoopbackHost, 0},
		{"port negative", LoopbackHost, -1},
		{"port too high", LoopbackHost, 65536},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildArgs(tt.host, tt.port, nil); err == nil {
				t.Fatalf("BuildArgs(%q, %d) = nil error, want rejection", tt.host, tt.port)
			}
		})
	}
}

func TestBuildArgsRejectsInvalidStrategy(t *testing.T) {
	if _, err := BuildArgs(LoopbackHost, 2081, []string{"--port", "9999"}); err == nil {
		t.Fatal("a strategy that moves the listener must not build")
	}
}

func TestValidateArgsAcceptsSupportedOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"split at sni", []string{"--split", "1+s"}},
		{"short split", []string{"-s", "1+s"}},
		{"negative offset", []string{"--split", "-1"}},
		{"repeats and skip", []string{"--split", "2:5:10"}},
		{"combined position flags", []string{"--disorder", "3+se"}},
		{"inline list value", []string{"--mod-http=h,d"}},
		{"inline auto", []string{"--auto=torst"}},
		{"separate auto", []string{"--auto", "torst,redirect"}},
		{"valueless option", []string{"--no-udp"}},
		{"two valueless options", []string{"--no-udp", "--no-domain"}},
		{"fake sni", []string{"--fake", "1+s", "--fake-sni", "www.example.com"}},
		{"fake sni placeholders", []string{"--fake-sni", "??????.com"}},
		{"numeric option", []string{"--ttl", "8"}},
		{"round range", []string{"--round", "1-3"}},
		{"tls mod with size", []string{"--fake-tls-mod=msize=1200"}},
		{"oob data char", []string{"--oob-data", "a"}},
		{"port filter range", []string{"--pf", "443-444"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateArgs(tt.args)
			if err != nil {
				t.Fatalf("ValidateArgs(%q): %v", tt.args, err)
			}
			if !equalArgs(got, tt.args) {
				t.Errorf("validated args = %q, want them returned as written %q", got, tt.args)
			}
		})
	}
}

// TestValidateArgsRejectsInjection covers the reason this validation exists:
// every token reaches the argv of a process we spawn, so anything a user or a
// subscription can influence has to be refused unless it is a known desync
// option with a value of the expected shape.
func TestValidateArgsRejectsInjection(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantHint string // substring the error must carry, so failures are legible
	}{
		{"moves the listener", []string{"--port", "9999"}, "--port"},
		{"moves the listener, short form", []string{"-p", "9999"}, "-p"},
		{"binds to the world", []string{"--ip", "0.0.0.0"}, "--ip"},
		{"rebinds outgoing connections", []string{"--conn-ip", "10.0.0.1"}, "--conn-ip"},
		{"reads an arbitrary file", []string{"--hosts", "/etc/passwd"}, "--hosts"},
		{"reads an arbitrary file, short form", []string{"-H", "/etc/shadow"}, "-H"},
		{"loads fake data from a file", []string{"--fake-data", "C:\\Windows\\win.ini"}, "--fake-data"},
		{"writes an arbitrary file", []string{"--cache-dump", "C:\\Users\\Public\\evil.bat"}, "--cache-dump"},
		{"unknown option", []string{"--exec", "calc.exe"}, "--exec"},
		{"bare word", []string{"calc.exe"}, "calc.exe"},
		{"shell metacharacters", []string{"; rm -rf /"}, ""},
		{"command substitution in a value", []string{"--split", "$(whoami)"}, "--split"},
		{"backticks in a value", []string{"--split", "`whoami`"}, "--split"},
		{"chained command in a value", []string{"--split", "1; calc.exe"}, "--split"},
		{"smuggled option inside a value", []string{"--split", "1 --port 9999"}, "--split"},
		{"smuggled option as a value", []string{"--split", "--port"}, "--split"},
		{"path traversal in a name", []string{"--fake-sni", "../../etc/passwd"}, "--fake-sni"},
		{"newline in a value", []string{"--split", "1\n--port 9999"}, "--split"},
		{"quote in a value", []string{"--split", "1\" --port \"9999"}, "--split"},
		{"value for a valueless option", []string{"--no-udp", "9999"}, "9999"},
		{"inline value for a valueless option", []string{"--no-udp=9999"}, "--no-udp"},
		{"missing value", []string{"--split"}, "--split"},
		{"trailing option without value", []string{"--split", "1+s", "--tlsrec"}, "--tlsrec"},
		{"empty token", []string{""}, "empty"},
		{"lone dash", []string{"-"}, "-"},
		{"lone double dash", []string{"--"}, "--"},
		{"out of range number", []string{"--ttl", "999999"}, "--ttl"},
		{"attached short value", []string{"-s1+s"}, "-s"},
		{"attached short value on a managed option", []string{"-p9999"}, "-p"},
		{"inline value on a short option", []string{"-s=1"}, "-s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateArgs(tt.args)
			if err == nil {
				t.Fatalf("ValidateArgs(%q) = %q, nil; want rejection", tt.args, got)
			}
			if got != nil {
				t.Errorf("rejected args returned %q, want nil", got)
			}
			if tt.wantHint != "" && !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error %q does not mention %q", err, tt.wantHint)
			}
		})
	}
}

func TestValidateArgsBoundsInput(t *testing.T) {
	many := make([]string, 0, maxStrategyArgs+2)
	for i := 0; i < maxStrategyArgs+2; i += 2 {
		many = append(many, "--split", "1+s")
	}
	if _, err := ValidateArgs(many); err == nil {
		t.Errorf("ValidateArgs with %d arguments = nil, want rejection", len(many))
	}

	long := []string{"--split", strings.Repeat("1", maxArgLen+1)}
	if _, err := ValidateArgs(long); err == nil {
		t.Error("ValidateArgs with an oversized value = nil, want rejection")
	}
}

func TestValidateArgsReturnsACopy(t *testing.T) {
	args := []string{"--split", "1+s"}
	got, err := ValidateArgs(args)
	if err != nil {
		t.Fatalf("ValidateArgs: %v", err)
	}
	got[1] = "tampered"
	if args[1] != "1+s" {
		t.Errorf("caller's slice was aliased and mutated to %q", args[1])
	}
}

func TestValidateArgsEmptyIsFine(t *testing.T) {
	// No strategy options at all is a valid strategy: ciadpi still proxies, just
	// without any desync. The daemon relies on that to run a "bypass off but
	// process up" shape during tests.
	got, err := ValidateArgs(nil)
	if err != nil {
		t.Fatalf("ValidateArgs(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ValidateArgs(nil) = %q, want empty", got)
	}
}
