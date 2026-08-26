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
	// Version guards the format. A new *additive* field needs no bump: it reads
	// back as its zero value from an older file, which the loading path turns into
	// the field's default. Bump it only for a change the reader must act on — a
	// renamed field, or a stored value whose meaning has changed — and give that
	// version a case in migrate(). A file whose version is newer than this reader
	// understands falls back to defaults rather than guessing.
	Version int `json:"version"`

	SplitMode string   `json:"split_mode,omitempty"`
	SplitApps []string `json:"split_apps,omitempty"`

	// RoutingMode remembers smart/global/direct. Without it a user who switched
	// to global got smart back on the next launch, with no indication that their
	// choice had been dropped — the UI reported the mode the daemon had reset to.
	RoutingMode string `json:"routing_mode,omitempty"`

	// PresetGamesDirect, PresetVoiceDirect and PresetUnblockServices remember the
	// three routing presets. They are *bool because absent and false are different
	// answers here: the defaults are per-preset (unblock-services on, the two that
	// take traffic out of the tunnel off), so "the file does not say" has to reach
	// SetSettings as nil and pick up that preset's default, while a stored value —
	// either value — is the user's choice and is honoured as written.
	PresetGamesDirect     *bool `json:"preset_games_direct,omitempty"`
	PresetVoiceDirect     *bool `json:"preset_voice_direct,omitempty"`
	PresetUnblockServices *bool `json:"preset_unblock_services,omitempty"`

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

	// ZapretStrategy remembers which DPI-bypass strategy was picked, so the
	// measurement that chose it is made once rather than at every launch.
	//
	// Without it a restart falls back to the bundle's default strategy, which on
	// this author's ISP is not the one that won: the probe run scored
	// "general (FAKE TLS AUTO)" at 4/5 targets while plain "general" — the
	// alphabetical default — did not. The user would then watch the app start,
	// report the bypass active, and still fail to load video, with nothing on
	// screen to suggest that a five-minute measurement had been quietly discarded.
	// Absent (an old file, or a bundle never probed) reads back empty, which keeps
	// the previous behaviour of launching the bundle default.
	ZapretStrategy string `json:"zapret_strategy,omitempty"`

	// ZapretAutoUpdate remembers whether the bundle updates itself. A *bool so the
	// default can be on without a version bump: absent (an old file, or one written
	// before the field existed) reads back as the on default, an explicit false is
	// the user's choice to hold a version. Same shape, same reason, as
	// AutoFailover.
	ZapretAutoUpdate *bool `json:"zapret_auto_update,omitempty"`

	// ZapretEnabled remembers the bypass switch itself — not which strategy, but
	// whether the user has the bypass on — so the daemon can put it back at
	// start-up (see raiseZapretOnStart).
	//
	// Without it the bypass came up from exactly one place: connecting. Someone
	// who runs the bypass on the direct channel with no tunnel — a legitimate
	// setup, the packet filter needs no exit node — was left with no bypass at all
	// after a service restart, an app update or a reboot, silently, until they
	// pressed the button themselves. Measured on this author's machine at thirteen
	// hours.
	//
	// A *bool, for the same reason CrashReports is one: absent (nil) means nobody
	// has ever touched the switch, and that is not "off". Starting a kernel packet
	// filter on a machine whose owner never asked for one would be a surprise, not
	// a restoration, so only a stored true raises anything. Being a pointer, an old
	// file written before the field existed reads back as nil — never asked — which
	// is exactly the pre-restore behaviour, so no version bump is needed to add it.
	ZapretEnabled *bool `json:"zapret_enabled,omitempty"`
}

// settingsVersion is the current persisted-settings format version.
//
// v1 -> v2 clears the two outward-routing presets once. 0.5.0 (the only released
// v1 writer) shipped preset_games_direct and preset_voice_direct on and persisted
// them through settingsLocked, yet gave no way to see or change them: no UI, no
// wire command, no field in the reported state. A stored true is therefore a
// default the user was never shown, not a choice, and v2 treats it as such — see
// migrateV1toV2. Every other field a v1 file carries is a genuine user choice and
// is preserved untouched.
const settingsVersion = 2

// migrate upgrades a freshly-decoded persistedSettings to the current version. It
// returns the zero value — read everywhere as "no preferences yet" — for a
// version this reader does not know how to read, so a downgrade never misreads a
// newer layout and a pre-versioning (version 0) file is not guessed at. Known old
// versions are walked forward to the current one; the returned struct always
// carries Version == settingsVersion.
func migrate(ps persistedSettings) persistedSettings {
	switch ps.Version {
	case settingsVersion:
		return ps
	case 1:
		return migrateV1toV2(ps)
	default:
		// version 0 (pre-versioning) or a future/unrecognised layout.
		return persistedSettings{}
	}
}

// migrateV1toV2 forces the two outward-routing presets off in a v1 file.
//
// In 0.5.0 both preset_games_direct and preset_voice_direct shipped on with no
// UI, wire command or reported state to see or change them, so a stored true is a
// default the user was never offered rather than a decision. Clearing both here
// closes the leak an upgraded install would otherwise keep — real-time UDP and
// game traffic escaping the tunnel — exactly as the new constructor defaults close
// it for a fresh install. They are set to an explicit false (not left absent) so
// the choice is recorded as made once the file is next saved at v2, and so a user
// who wants either back turns it on through the switch 0.5.0 never had.
//
// preset_unblock_services is deliberately left as written: it only ever pins
// domains *into* the tunnel, so its 0.5.0 default carries no leak to undo. Every
// other field is a real user choice (split, kill switch, DNS, rules, ...) and is
// preserved.
func migrateV1toV2(ps persistedSettings) persistedSettings {
	gamesOff, voiceOff := false, false
	ps.PresetGamesDirect = &gamesOff
	ps.PresetVoiceDirect = &voiceOff
	ps.Version = settingsVersion
	return ps
}

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
// the zero value. The decoded settings are run through migrate, which walks a
// known older version forward and treats an unknown (future or pre-versioning)
// one as no settings, so a downgrade can't misinterpret a newer layout. A
// migration is applied in memory on every load until the next Save rewrites the
// file at the current version; it is idempotent, so re-migrating an as-yet-unsaved
// file is harmless.
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
	return migrate(ps)
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
