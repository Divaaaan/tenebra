package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Divaaaan/tenebra/core/routing"
)

// settingsFile is the name of the JSON file persisting user routing preferences
// inside the daemon's config directory. It is separate from lastgood.json: that
// caches node selection, this remembers explicit user choices (the split config)
// so they survive a restart.
const settingsFile = "settings.json"

// persistedSettings is the on-disk shape: the split configuration, the kill
// switch, and the tun stack. The struct is versioned so new preference fields
// can be added without a migration. A field added later defaults to its zero
// value when reading an old file, which the loading path then turns into the
// sane default.
type persistedSettings struct {
	// Version guards the format. Bump it only on an incompatible change; readers
	// of an unknown future version fall back to defaults rather than guessing.
	Version int `json:"version"`

	SplitMode string   `json:"split_mode,omitempty"`
	SplitApps []string `json:"split_apps,omitempty"`

	// KillSwitch remembers whether the user armed the kill switch. Absent in
	// files written before the field existed, which reads back as false — off,
	// matching the pre-kill-switch behaviour.
	KillSwitch bool `json:"kill_switch,omitempty"`
	// TLSFragment remembers whether the user armed forced TLS ClientHello
	// fragmentation. Absent in files written before the field existed, which reads
	// back as false — off, matching the pre-fragmentation behaviour.
	TLSFragment bool `json:"tls_fragment,omitempty"`
	// TunStack remembers the chosen tun network stack (system/gvisor/mixed).
	// Absent or unrecognized keeps the default; SetSettings validates it.
	TunStack string `json:"tun_stack,omitempty"`

	// ProxyMode remembers the connection mode ("tun"/"system-proxy"), and ProxyPort
	// the mixed inbound's loopback port. Absent in files written before the fields
	// existed reads back as "" / 0, which SetSettings turns into the tun default and
	// the default port — matching the pre-system-proxy behaviour. An unrecognized
	// mode keeps the default, like TunStack.
	ProxyMode string `json:"proxy_mode,omitempty"`
	ProxyPort int    `json:"proxy_port,omitempty"`

	// Autoconnect remembers whether the daemon reconnects the last profile when
	// it starts (see AutoconnectOnStart). Absent reads back as false — off,
	// matching the pre-autoconnect behaviour.
	Autoconnect bool `json:"autoconnect,omitempty"`

	// AutoFailover remembers whether the health watchdog is armed. It is a *bool
	// so the default can be on without a version bump: absent (nil) — an old file,
	// or one written before the field existed — reads back as the on default,
	// while an explicit true/false is the user's stored choice. This mirrors how
	// CrashReports uses a pointer to keep "unset" distinct from "off".
	AutoFailover *bool `json:"auto_failover,omitempty"`

	// AdBlock remembers whether the user armed DNS ad/tracker blocking. Absent in
	// files written before the field existed, which reads back as false — off,
	// matching the pre-ad-block behaviour.
	AdBlock bool `json:"ad_block,omitempty"`
	// DNSRemote and DNSDirect remember custom resolvers. Absent or empty reads back
	// as "" and the loading path lets Normalize substitute the default resolver, so
	// an old file (or a user who never changed them) transparently keeps the
	// defaults.
	DNSRemote string `json:"dns_remote,omitempty"`
	DNSDirect string `json:"dns_direct,omitempty"`
	// IPv4Only remembers whether the user pinned DNS to IPv4-only. Absent in files
	// written before the field existed, which reads back as false — off, matching
	// the pre-IPv4-only behaviour.
	IPv4Only bool `json:"ipv4_only,omitempty"`

	// RulesDirect and RulesProxy remember the custom domain-suffix routing rules.
	// Absent in files written before the field existed, which reads back as empty.
	RulesDirect []string `json:"rules_direct,omitempty"`
	RulesProxy  []string `json:"rules_proxy,omitempty"`
	// PresetRuBanking and PresetRuGov remember whether the bundled Russian
	// banking / government direct-rule presets are on. Absent reads back as false —
	// off, matching the pre-preset behaviour.
	PresetRuBanking bool `json:"preset_ru_banking,omitempty"`
	PresetRuGov     bool `json:"preset_ru_gov,omitempty"`
	// Multihop, MultihopEntryID and MultihopExitID remember the two-hop chain
	// selection: the toggle and the stable server IDs of the entry and exit nodes.
	// Absent in files written before the field existed reads back as off with no
	// selection, matching the pre-multihop behaviour. The IDs are stored even when
	// the toggle is off so re-enabling restores the last pick.
	Multihop        bool   `json:"multihop,omitempty"`
	MultihopEntryID string `json:"multihop_entry_id,omitempty"`
	MultihopExitID  string `json:"multihop_exit_id,omitempty"`
	// CrashReports remembers the crash-report consent. A *bool so the three
	// states stay distinct across a restart: absent (nil) means the user has not
	// been asked, so the GUI still shows the first-run prompt; a stored true/false
	// is their explicit choice. Being a pointer, an old file written before the
	// field existed reads back as nil — "not asked" — so no version bump is
	// needed to add it.
	CrashReports *bool `json:"crash_reports,omitempty"`
	// LastProfile and LastNode record the last successful user-commanded
	// connect: the profile, and the node only when the request pinned an
	// explicit exit (empty when the fallback walk chose). Autoconnect re-issues
	// exactly this intent at the next daemon start. A vanished profile or node
	// is tolerated at load time and simply leaves the daemon idle.
	LastProfile string `json:"last_profile,omitempty"`
	LastNode    string `json:"last_node,omitempty"`
}

// settingsVersion is the current persisted-settings format version.
const settingsVersion = 1

// settingsStore is the interface the daemon uses to persist and load user
// preferences. It is small so tests can substitute an in-memory fake and the
// daemon needn't import the file machinery directly.
type settingsStore interface {
	// Load returns the persisted settings, or the zero value if none exist or the
	// file is unreadable. It never errors: a missing or corrupt file is treated as
	// "no preferences yet".
	Load() persistedSettings
	// Save writes the settings atomically. A write error is returned so the caller
	// can log it, but persistence is best-effort and must not break the session.
	Save(s persistedSettings) error
}

// fileSettings is a disk-backed settingsStore. The file is rewritten atomically
// (temp file + fsync + rename) on every Save so a crash mid-write never leaves a
// corrupt file. It is safe for concurrent use.
type fileSettings struct {
	path string
	mu   sync.Mutex
}

// OpenFileSettings binds a persistent settings store to dir, creating the
// directory if needed. A missing file is not an error; it surfaces as empty
// settings on Load. main installs the result on the daemon before serving.
func OpenFileSettings(dir string) (*fileSettings, error) {
	if dir == "" {
		return nil, errors.New("control: empty settings directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("control: create settings dir: %w", err)
	}
	return &fileSettings{path: filepath.Join(dir, settingsFile)}, nil
}

// Load reads the backing file, tolerating a missing or corrupt file by returning
// the zero value. An unknown version is treated as no settings so a downgrade
// can't misinterpret a newer layout.
func (s *fileSettings) Load() persistedSettings {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil || len(data) == 0 {
		return persistedSettings{}
	}
	var ps persistedSettings
	if err := json.Unmarshal(data, &ps); err != nil {
		return persistedSettings{} // corrupt: start fresh
	}
	if ps.Version != settingsVersion {
		return persistedSettings{}
	}
	return ps
}

// Save writes s to disk atomically: serialise to a temp file in the same
// directory, fsync it, then rename over the target so a reader never sees a
// half-written file.
func (s *fileSettings) Save(ps persistedSettings) error {
	ps.Version = settingsVersion

	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return fmt.Errorf("control: encode settings: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, settingsFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("control: create settings temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("control: write settings temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("control: sync settings temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close settings temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("control: replace settings file: %w", err)
	}
	return nil
}

// splitFromSettings turns persisted settings into a routing split config,
// normalised. Unknown/empty values collapse to off via Normalize, so a corrupt
// or partial file degrades to "no split" rather than an error.
func splitFromSettings(ps persistedSettings) (routing.SplitMode, []string) {
	o := routing.Options{
		Mode:      routing.ModeSmart, // placeholder; only the split fields are read back
		SplitMode: routing.SplitMode(ps.SplitMode),
		SplitApps: ps.SplitApps,
	}.Normalize()
	return o.SplitMode, o.SplitApps
}

var _ settingsStore = (*fileSettings)(nil)
