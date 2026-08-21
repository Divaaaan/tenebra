package zapret

import (
	"strings"
	"testing"
)

// launchLine is the shape every shipped strategy uses to start winws: one long
// line with the capture filter first and the desync blocks after it.
const launchLine = `start "zapret: %~n0" /min "%BIN%winws.exe" --wf-tcp=80,443 --wf-udp=443,50000-50100 ^`

func batWith(line string) string {
	return "@echo off\r\nchcp 65001 > nul\r\n" + line + "\r\n--filter-tcp=443 --dpi-desync=fake --new\r\n"
}

// TestPinInterfaceConfinesTheFilter is the fix itself: unpinned, winws also
// filters the tunnel's adapter, where its fake packets corrupt the handshakes it
// exists to protect.
func TestPinInterfaceConfinesTheFilter(t *testing.T) {
	out, changed := PinInterface(batWith(launchLine), 18)
	if !changed {
		t.Fatal("the launch line was left unpinned")
	}
	if !strings.Contains(out, "--wf-iface=18 --wf-tcp=80,443") {
		t.Errorf("the pin is not in front of the capture filter:\n%s", out)
	}
	// Everything else has to survive byte for byte: the blocks after the filter
	// are what actually carry YouTube.
	if !strings.Contains(out, `--filter-tcp=443 --dpi-desync=fake --new`) {
		t.Error("a desync block was lost")
	}
	if !strings.Contains(out, `"%BIN%winws.exe"`) {
		t.Error("the binary path was mangled")
	}
}

// TestPinInterfaceLeavesAnAlreadyPinnedBatchAlone: a bundle that pins the
// interface itself knows better than we do.
func TestPinInterfaceLeavesAnAlreadyPinnedBatchAlone(t *testing.T) {
	pinned := `start "zapret" /min "%BIN%winws.exe" --wf-iface=7 --wf-tcp=80,443 ^`
	out, changed := PinInterface(batWith(pinned), 18)
	if changed {
		t.Error("an already pinned batch was rewritten")
	}
	if strings.Contains(out, "--wf-iface=18") {
		t.Error("a second pin was added")
	}
}

// TestPinInterfaceRefusesToGuess: without a known index, filtering everything is
// wrong but filtering a guessed interface is worse — it bypasses nothing while
// looking like it works.
func TestPinInterfaceRefusesToGuess(t *testing.T) {
	for _, idx := range []int{0, -1} {
		out, changed := PinInterface(batWith(launchLine), idx)
		if changed {
			t.Errorf("index %d produced a pin", idx)
		}
		if !strings.Contains(out, "--wf-tcp=80,443") {
			t.Errorf("index %d altered the batch", idx)
		}
	}
}

// TestPinInterfaceIgnoresLinesThatDoNotLaunchWinws guards against pinning a
// comment or an echo that merely mentions the flags.
func TestPinInterfaceIgnoresLinesThatDoNotLaunchWinws(t *testing.T) {
	bat := "@echo off\r\n:: --wf-tcp=80,443 is the capture filter\r\necho --wf-udp=443\r\n"
	if _, changed := PinInterface(bat, 18); changed {
		t.Error("a batch with no launch line was rewritten")
	}
}

// TestPinInterfaceComposesWithTheVoiceStrip: both rewrites land on the same
// derived batch, and neither undoes the other.
func TestPinInterfaceComposesWithTheVoiceStrip(t *testing.T) {
	bat := "@echo off\r\n" +
		`start "zapret" /min "%BIN%winws.exe" --wf-tcp=80,443 --wf-udp=443,19294-19344,50000-50100 ^` + "\r\n" +
		`--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --new ^` + "\r\n" +
		`--filter-tcp=443 --dpi-desync=fake --new` + "\r\n"

	stripped, ok := StripVoiceUDP(bat)
	if !ok {
		t.Fatal("the voice block was not removed")
	}
	out, ok := PinInterface(stripped, 18)
	if !ok {
		t.Fatal("the stripped batch was not pinned")
	}
	if !strings.Contains(out, "--wf-iface=18 --wf-tcp=80,443") {
		t.Error("the pin did not survive the strip")
	}
	if strings.Contains(out, "--filter-l7=discord,stun") {
		t.Error("the pin brought the voice block back")
	}
	if strings.Contains(out, "19294-19344,50000-50100,") {
		t.Error("the voice ports are still captured")
	}
}
