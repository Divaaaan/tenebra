package control

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// SupportBundle is the result of the collect_diagnostics command: one block of
// text the user can save and attach to a bug report, plus the filename to
// suggest for it.
//
// The core assembles the text but does not write the file. In service mode its
// own data directory is readable only by SYSTEM and Administrators, so a file
// dropped there would be one the person filing the report cannot open — the
// caller (the desktop shell) writes it somewhere the user actually lives.
type SupportBundle struct {
	// Text is the assembled report, already scrubbed of secrets.
	Text string `json:"text"`
	// Filename is a timestamped name to save it under. It carries no path.
	Filename string `json:"filename"`
}

// supportLogLines is how many trailing log lines the bundle carries. Enough to
// cover a whole connect walk with its per-candidate probe results, capped so the
// file stays something a person can read and a chat can accept.
const supportLogLines = 300

// versionedRunner is implemented by runners that can report the sing-box build
// they launch. It is an optional interface rather than a Runner method so the
// fakes in this package's tests — and any future runner — stay valid without
// having to answer a question they have no way to answer.
type versionedRunner interface {
	SingboxVersion() string
}

// handleCollectDiagnostics assembles the support bundle.
func (d *Daemon) handleCollectDiagnostics(req Request) Response {
	resp, err := newResult(req.ID, d.CollectDiagnostics())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// CollectDiagnostics assembles everything a maintainer needs to read a failure
// without the reporter having to describe it: what the daemon thinks its state
// is, what it is built from, what the machine's routing looks like, what
// sing-box last said, and the tail of the log.
//
// It reads only what the daemon already has plus one interface enumeration; it
// starts no probe, touches no network, and sends nothing anywhere. Everything is
// run through scrubSecrets on the way out.
func (d *Daemon) CollectDiagnostics() SupportBundle {
	now := time.Now()
	d.mu.Lock()
	if d.now != nil {
		now = d.now()
	}
	level := d.logLevel
	tail := d.logTail
	attempts := d.attempts
	multihop := d.multihop
	d.mu.Unlock()

	st := d.snapshotState()
	ro := d.snapshotRouting()
	tun := d.snapshotTun()

	var b strings.Builder
	section := func(title string) {
		b.WriteString("\n")
		b.WriteString(title)
		b.WriteString("\n")
		b.WriteString(strings.Repeat("-", len(title)))
		b.WriteString("\n")
	}

	b.WriteString("Tenebra core diagnostics\n")
	b.WriteString("========================\n")
	fmt.Fprintf(&b, "Generated:     %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Core version:  %s\n", buildinfo.Version)
	fmt.Fprintf(&b, "sing-box:      %s\n", orUnknown(d.singboxVersion()))
	fmt.Fprintf(&b, "Bypass bundle: %s\n", orUnknown(st.ZapretVersion))
	fmt.Fprintf(&b, "Platform:      %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "Log level:     %s\n", level)
	if d.store != nil {
		fmt.Fprintf(&b, "Store:         %s\n", d.store.Dir())
	}

	section("State")
	fmt.Fprintf(&b, "State:         %s\n", st.State)
	fmt.Fprintf(&b, "Profile:       %s\n", orNone(st.Profile))
	fmt.Fprintf(&b, "Node:          %s\n", orNone(st.Node))
	if st.Error != "" {
		fmt.Fprintf(&b, "Error:         %s\n", st.Error)
	}
	fmt.Fprintf(&b, "Routing:       %s\n", orNone(st.Routing))
	fmt.Fprintf(&b, "Mode:          %s (stack %s, mixed port %d)\n", orNone(st.ProxyMode), orNone(st.TunStack), st.ProxyPort)
	fmt.Fprintf(&b, "Kill switch:   %s\n", onOff(st.KillSwitch))
	fmt.Fprintf(&b, "TLS fragment:  %s\n", onOff(st.TLSFragment))
	fmt.Fprintf(&b, "Auto failover: %s\n", onOff(st.AutoFailover))
	fmt.Fprintf(&b, "Autoconnect:   %s\n", onOff(st.Autoconnect))
	fmt.Fprintf(&b, "Ad block:      %s\n", onOff(st.AdBlock))
	fmt.Fprintf(&b, "IPv4 only:     %s\n", onOff(st.IPv4Only))
	fmt.Fprintf(&b, "DNS:           remote %s / direct %s\n", orNone(st.DNSRemote), orNone(st.DNSDirect))
	fmt.Fprintf(&b, "Split:         %s (%d apps)\n", orNone(st.Split), len(st.SplitApps))
	fmt.Fprintf(&b, "Custom rules:  %d direct / %d proxied\n", len(ro.RulesDirect), len(ro.RulesProxy))
	fmt.Fprintf(&b, "Presets:       games %s, voice %s, unblock %s, ru-banking %s, ru-gov %s\n",
		onOff(ro.GamesDirect), onOff(ro.VoiceDirect), onOff(ro.UnblockServices),
		onOff(ro.PresetRuBanking), onOff(ro.PresetRuGov))
	fmt.Fprintf(&b, "Multihop:      %s\n", multihopLine(multihop.Enabled, multihop.EntryID, multihop.ExitID))
	fmt.Fprintf(&b, "Bypass:        %s (strategy %s, auto-update %s)\n",
		onOff(st.ZapretActive), orNone(st.ZapretStrategy), onOff(st.ZapretAutoUpdate))
	fmt.Fprintf(&b, "Tun address:   %s\n", orNone(tun.Address))

	section("Profiles")
	profiles := d.store.List()
	if len(profiles) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range profiles {
		fmt.Fprintf(&b, "%-20s %-28s nodes=%-3d managed=%-5t tier=%s updated=%s\n",
			p.ID, truncate(p.Name, 28), len(p.Servers), p.Managed, orNone(p.Tier),
			p.UpdatedAt.UTC().Format(time.RFC3339))
	}

	section("Last connect walk")
	if attempts == nil || len(attempts.Items) == 0 {
		b.WriteString("(no walk recorded since the last teardown)\n")
	} else {
		fmt.Fprintf(&b, "outcome: %s\n", orNone(attempts.Outcome))
		for _, it := range attempts.Items {
			fmt.Fprintf(&b, "%2d. %-10s %-20s %-8s", it.Seq, it.Protocol, it.Node, it.Status)
			if it.LastGood {
				b.WriteString(" last-good")
			}
			if it.Strategy != "" {
				fmt.Fprintf(&b, " strategy=%s", it.Strategy)
			}
			if it.Reason != "" {
				fmt.Fprintf(&b, " reason=%s", it.Reason)
			}
			b.WriteString("\n")
		}
	}

	section("Interfaces and default routes")
	b.WriteString(interfaceTable(d.probeInterfaces()))

	section("sing-box output (tail)")
	sb := d.runner.Logs()
	if len(sb) == 0 {
		b.WriteString("(nothing captured)\n")
	}
	for _, ln := range sb {
		b.WriteString(ln)
		b.WriteString("\n")
	}

	// The file tail comes first because it carries the process-level lines the
	// ring never sees (start-up, store paths, the reason a fatal exit happened),
	// and it survives a restart that empties the ring.
	if tail != nil {
		lines := tail(supportLogLines)
		section(fmt.Sprintf("Log file (last %d lines)", len(lines)))
		if len(lines) == 0 {
			b.WriteString("(empty)\n")
		}
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteString("\n")
		}
	}

	entries := d.logs.snapshot()
	if len(entries) > supportLogLines {
		entries = entries[len(entries)-supportLogLines:]
	}
	section(fmt.Sprintf("Core log (last %d lines)", len(entries)))
	if len(entries) == 0 {
		b.WriteString("(empty)\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s] %-5s %s\n", e.At.UTC().Format(time.RFC3339), strings.ToUpper(e.Level), e.Msg)
	}

	return SupportBundle{
		Text:     scrubSecrets(b.String()),
		Filename: fmt.Sprintf("tenebra-diagnostics-%s.txt", now.UTC().Format("20060102-150405")),
	}
}

// probeInterfaces enumerates the machine's interfaces for the bundle, or returns
// nil when the platform has no enumerator wired (the same nil the tun-conflict
// guard treats as "nothing known").
func (d *Daemon) probeInterfaces() []tunguard.Iface {
	d.mu.Lock()
	probe := d.ifaceProbe
	d.mu.Unlock()
	if probe == nil {
		return nil
	}
	ifaces, err := probe()
	if err != nil {
		return nil
	}
	return ifaces
}

// interfaceTable renders the route/adapter view: which interfaces exist, which
// of them look like tunnels, and which carry a default route at what metric.
// That is the whole input the tun-conflict guard reasons about, so a bundle
// showing it lets a maintainer re-derive the guard's verdict rather than take
// the user's word for what else was running.
func interfaceTable(ifaces []tunguard.Iface) string {
	if len(ifaces) == 0 {
		return "(no enumeration on this platform, or the route table could not be read)\n"
	}
	sorted := append([]tunguard.Iface(nil), ifaces...)
	// Default-route holders first, best metric first: the order in which the
	// stack would prefer them, which is the order that explains a conflict.
	sort.SliceStable(sorted, func(a, b int) bool {
		ma, da := sorted[a].BestMetric()
		mb, db := sorted[b].BestMetric()
		if da != db {
			return da
		}
		return ma < mb
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%-32s %-8s %-9s %s\n", "interface", "tunnel", "default", "metric")
	for _, ifc := range sorted {
		m, hasDef := ifc.BestMetric()
		metric := "-"
		if hasDef {
			metric = fmt.Sprint(m)
		}
		fmt.Fprintf(&b, "%-32s %-8s %-9s %s\n",
			truncate(ifc.Name, 32), yesNo(tunguard.IsTunnelIface(ifc)), yesNo(hasDef), metric)
	}
	return b.String()
}

// singboxVersion asks the runner what sing-box build it launches, when it can
// answer. An empty string means the runner does not report one (a fake, or a
// binary that could not be executed).
func (d *Daemon) singboxVersion() string {
	if vr, ok := d.runner.(versionedRunner); ok {
		return vr.SingboxVersion()
	}
	return ""
}

// multihopLine renders the two-hop selection in one line.
func multihopLine(enabled bool, entry, exit string) string {
	if !enabled {
		return "off"
	}
	return fmt.Sprintf("on (%s -> %s)", orNone(entry), orNone(exit))
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// truncate clips s to n characters so a long profile or adapter name cannot
// break the column layout.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
