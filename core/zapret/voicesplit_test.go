package zapret

import "strings"

import "testing"

// realStrategy is the shape a shipped strategy has, reduced to the lines that
// matter here: the WinDivert capture filter, the voice block, and one of the
// blocks that carries YouTube.
const realStrategy = `@echo off
chcp 65001 > nul
cd /d "%~dp0"
set "BIN=%~dp0bin\"
set "LISTS=%~dp0lists\"
start "zapret: %~n0" /min "%BIN%winws.exe" --wf-tcp=80,443,2053,2083,2087,2096,8443,%GameFilterTCP% --wf-udp=443,19294-19344,50000-50100,%GameFilterUDP% ^
--filter-udp=443 --hostlist="%LISTS%list-general.txt" --dpi-desync=fake --dpi-desync-repeats=11 --new ^
--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord="%BIN%ACTIVE_DISCORD_UDP.bin" --dpi-desync-fake-stun="%BIN%ACTIVE_DISCORD_UDP.bin" --dpi-desync-repeats=6 --new ^
--filter-tcp=443 --hostlist="%LISTS%list-google.txt" --dpi-desync=fake,multidisorder --new ^
--filter-udp=%GameFilterUDP% --ipset="%LISTS%ipset-all.txt" --dpi-desync=fake --dpi-desync-cutoff=n2
`

func TestStripVoiceUDPRemovesTheBlockThatStealsVoice(t *testing.T) {
	got, changed := StripVoiceUDP(realStrategy)
	if !changed {
		t.Fatal("reported no change on a strategy that carries the voice block")
	}

	// The block itself must be gone: while it runs, WinDivert reinjects Discord's
	// voice on the direct path, where the voice servers are blocked, and the client
	// reports "no route" no matter how the tunnel is routed.
	if strings.Contains(got, "--filter-l7=discord,stun") {
		t.Error("the discord/stun block survived")
	}
	if strings.Contains(got, "ACTIVE_DISCORD_UDP.bin") {
		t.Error("the block's fake payloads survived, so the block did")
	}

	// And the ports must leave the capture filter, so those packets are never
	// intercepted at all.
	if strings.Contains(got, "--wf-udp=443,19294-19344,50000-50100") {
		t.Error("the voice ports are still in the WinDivert capture filter")
	}
	if !strings.Contains(got, "--wf-udp=443,%GameFilterUDP%") {
		t.Errorf("the capture filter lost more than the voice ports:\n%s", got)
	}
}

// TestStripVoiceUDPKeepsEverythingElse: the other blocks are what carry YouTube,
// which is the whole reason the bypass runs. Removing the voice block must not
// cost the user the thing that was working.
func TestStripVoiceUDPKeepsEverythingElse(t *testing.T) {
	got, _ := StripVoiceUDP(realStrategy)

	for _, keep := range []string{
		`--wf-tcp=80,443,2053,2083,2087,2096,8443,%GameFilterTCP%`,
		`--filter-udp=443 --hostlist="%LISTS%list-general.txt"`,
		`--filter-tcp=443 --hostlist="%LISTS%list-google.txt"`,
		`--filter-udp=%GameFilterUDP%`,
		`set "BIN=%~dp0bin\"`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost a line that must survive: %s", keep)
		}
	}

	// One line removed, no others.
	before := strings.Count(realStrategy, "\n")
	after := strings.Count(got, "\n")
	if before-after != 1 {
		t.Errorf("removed %d lines, want exactly 1", before-after)
	}
}

// TestDiscoverSkipsTheDerivedStrategy: the rewritten copy lives in the bundle
// directory (it has to, for the batch's own path resolution to work), so the
// listing must not mistake it for a strategy of its own — the user would be
// offered the same bypass twice and a probe run would measure it twice.
func TestDiscoverSkipsTheDerivedStrategy(t *testing.T) {
	got := Discover("D:/zapret", []string{"general.bat", derivedStrategyFile, "general (ALT).bat"})
	if len(got) != 2 {
		t.Fatalf("found %d strategies, want 2: %+v", len(got), got)
	}
	for _, s := range got {
		if strings.HasPrefix(s.Name, ".") {
			t.Errorf("derived file offered as a strategy: %q", s.Name)
		}
	}
}

// TestStripVoiceUDPLeavesAStrategyWithoutTheBlockAlone: not every strategy ships
// the voice block, and rewriting one that has nothing to rewrite would mean
// launching a derived file for no reason.
func TestStripVoiceUDPLeavesAStrategyWithoutTheBlockAlone(t *testing.T) {
	plain := `@echo off
start "zapret" /min "%BIN%winws.exe" --wf-tcp=80,443 --wf-udp=443 ^
--filter-tcp=443 --hostlist="%LISTS%list-google.txt" --dpi-desync=fake
`
	got, changed := StripVoiceUDP(plain)
	if changed {
		t.Error("reported a change on a strategy that has no voice block")
	}
	if got != plain {
		t.Error("rewrote a strategy it had nothing to change")
	}
}
