//go:build !linux

package main

import (
	"os"
	"path/filepath"
)

// ruleSetCandidates lists the directories the bundled RU rule-sets may live in,
// in probe order. Off Linux there is exactly one: the directory holding the
// sing-box the core was pointed at. Both shipping layouts — the Windows install
// folder and the macOS app bundle — stage the .srs beside the binary as declared
// resources, so if they are not there they are nowhere.
//
// With TENEBRA_SINGBOX unset there is no resources directory to probe and the
// list is empty, which leaves the remote-download fallback in place rather than
// probing the process's working directory.
func ruleSetCandidates() []string {
	bin := os.Getenv("TENEBRA_SINGBOX")
	if bin == "" {
		return nil
	}
	return []string{filepath.Dir(bin)}
}
