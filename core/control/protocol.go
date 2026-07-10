// Package control implements the line-delimited JSON protocol the desktop UI
// uses to drive the core. It owns the connection state machine and the profile
// store but stays free of any OS tunnel code: the actual sing-box process is
// hidden behind the Runner interface, which an adapter package implements. That
// keeps the protocol layer fully unit-testable with a fake runner and avoids an
// import cycle between control and adapters.
//
// The wire format is one JSON object per line, UTF-8. Three message kinds flow
// over the link, all defined in docs/control-protocol.md:
//
//   - requests  (UI -> core) carry an id and a cmd;
//   - responses (core -> UI) echo the id with ok plus data or error;
//   - events    (core -> UI) are unsolicited, carry no id, and name an event.
package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Command names. These are the cmd values a Request may carry.
const (
	CmdStatus             = "status"
	CmdListProfiles       = "list_profiles"
	CmdImportSubscription = "import_subscription"
	CmdImportLink         = "import_link"
	CmdImportLinks        = "import_links"
	CmdRemoveProfile      = "remove_profile"
	CmdRefreshSub         = "refresh_subscription"
	CmdConnect            = "connect"
	CmdDisconnect         = "disconnect"
	CmdPing               = "ping"
	CmdSetRouting         = "set_routing"
	CmdSetSplit           = "set_split"
	CmdSetKillSwitch      = "set_kill_switch"
	CmdSetTun             = "set_tun"
	CmdSetAutoconnect     = "set_autoconnect"
	CmdSetDNS             = "set_dns"
	CmdLeakCheck          = "leak_check"
)

// ConnState is the connection lifecycle state reported in State and state
// events.
type ConnState string

const (
	StateIdle       ConnState = "idle"
	StateConnecting ConnState = "connecting"
	StateConnected  ConnState = "connected"
	StateError      ConnState = "error"
)

// Event names.
const (
	EventState    = "state"
	EventTraffic  = "traffic"
	EventLog      = "log"
	EventProfiles = "profiles"
)

// Log levels for log events.
const (
	LogInfo  = "info"
	LogWarn  = "warn"
	LogError = "error"
)

// Request is a UI -> core message. The fields used depend on Cmd; unused ones
// stay zero. Raw payloads are decoded straight into this flat struct because the
// command set is small and the fields don't collide.
type Request struct {
	ID  int64  `json:"id"`
	Cmd string `json:"cmd"`

	// Profile is the target profile ID for connect, disconnect (ignored),
	// remove_profile, refresh_subscription and ping.
	Profile string `json:"profile,omitempty"`
	// Node is the optional explicit node ID for connect.
	Node string `json:"node,omitempty"`
	// Auto selects the "fastest node" strategy for connect: when set and no Node
	// is given, the daemon pings every candidate and walks them by ascending RTT
	// (fastest first) instead of by protocol preference. It is ignored when Node
	// is present (an explicit exit wins) and defaults to false, so an omitted
	// field preserves the original protocol-fallback behaviour exactly.
	Auto bool `json:"auto,omitempty"`
	// URL is the subscription URL for import_subscription.
	URL string `json:"url,omitempty"`
	// Link is the single share link for import_link.
	Link string `json:"link,omitempty"`
	// Links is the batch of share links for import_links. Each element may itself
	// be a multi-line string (a pasted block) or a single link; the daemon splits
	// on newlines, drops blanks/comments/duplicates, and counts unparseable lines
	// as skipped rather than failing the whole import.
	Links []string `json:"links,omitempty"`
	// Name names the profile for import_subscription, import_link and import_links.
	Name string `json:"name,omitempty"`
	// Mode is the routing mode for set_routing (smart/global/direct) and also the
	// split mode for set_split (off/exclude/include).
	Mode string `json:"mode,omitempty"`
	// Apps is the executable-name list for set_split, e.g. ["chrome.exe"].
	Apps []string `json:"apps,omitempty"`
	// On arms (true) or disarms (false) the kill switch for set_kill_switch, and
	// likewise the connect-on-start preference for set_autoconnect. An omitted
	// field decodes to false, so "disarm" and "field left out" coincide — which
	// is the safe reading for a command that grants, not removes, behaviour.
	On bool `json:"on,omitempty"`
	// Stack is the tun network stack for set_tun (system/gvisor/mixed).
	Stack string `json:"stack,omitempty"`
	// AdBlock arms (true) or disarms (false) DNS ad/tracker blocking for set_dns.
	// Like On, an omitted field decodes to false — the safe "off" reading for an
	// opt-in feature.
	AdBlock bool `json:"ad_block,omitempty"`
	// DNSRemote and DNSDirect are the custom resolvers for set_dns: the encrypted
	// resolver reached over the proxy for general lookups, and the direct resolver
	// for destinations kept off the tunnel. Each accepts the schemes the DNS
	// builder parses (tls/https/quic/h3/tcp/udp, or a bare host); an empty field
	// resets that resolver to its default.
	DNSRemote string `json:"dns_remote,omitempty"`
	DNSDirect string `json:"dns_direct,omitempty"`
}

// Response is a core -> UI reply to a Request, correlated by ID. Exactly one of
// Data or Error is meaningful: Data when Ok, Error otherwise. Data is held as
// raw JSON so the server can attach any command's result without this type
// needing to know each shape.
type Response struct {
	ID    int64           `json:"id"`
	Ok    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Event is an unsolicited core -> UI message. It carries no id. The payload is
// raw JSON whose shape is fixed by Event (see the State/Traffic/Log event
// helpers).
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"-"`
}

// State is the connection status object. It is both the status command's result
// and the body of a state event, matching the State type in the protocol doc.
type State struct {
	State   ConnState `json:"state"`
	Node    string    `json:"node,omitempty"`
	Profile string    `json:"profile,omitempty"`
	Routing string    `json:"routing,omitempty"`
	// Split is the per-app split mode (off/exclude/include) and SplitApps the
	// executable names it applies to. Both reflect the config that the next
	// connect will use; an empty/off split omits them.
	Split     string   `json:"split,omitempty"`
	SplitApps []string `json:"split_apps,omitempty"`
	// KillSwitch reports whether the kill switch is armed (strict_route on the
	// tun, plus an automatic relaunch if the tunnel process dies). Omitted when
	// off, like the split fields.
	KillSwitch bool `json:"kill_switch,omitempty"`
	// TunStack is the tun network stack (system/gvisor/mixed) the current or
	// next tunnel uses. Always present once the daemon has normalized it.
	TunStack string `json:"tun_stack,omitempty"`
	// Autoconnect reports whether the daemon reconnects the last profile when it
	// starts (see AutoconnectOnStart). Omitted when off, like the kill switch.
	Autoconnect bool `json:"autoconnect,omitempty"`
	// AdBlock reports whether DNS ad/tracker blocking is armed. Omitted when off,
	// like the kill switch.
	AdBlock bool `json:"ad_block,omitempty"`
	// DNSRemote and DNSDirect report the resolvers the current or next tunnel uses
	// (the encrypted proxied resolver and the direct one). Always present once the
	// daemon has normalized them, so the UI can prefill the custom-DNS inputs with
	// the effective values — the configured ones, or the defaults when unset.
	DNSRemote string `json:"dns_remote,omitempty"`
	DNSDirect string `json:"dns_direct,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PingResult is one node's dial-latency probe outcome.
type PingResult struct {
	Node  string `json:"node"`
	RTTMs int64  `json:"rttMs"`
	Ok    bool   `json:"ok"`
}

// TrafficEvent is the body of a traffic event: cumulative byte counters plus the
// per-second rates computed from the last poll delta.
type TrafficEvent struct {
	Up       int64 `json:"up"`
	Down     int64 `json:"down"`
	UpRate   int64 `json:"up_rate"`
	DownRate int64 `json:"down_rate"`
}

// LogEvent is the body of a log event.
type LogEvent struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// newResult builds a successful Response for id, marshalling v as the data
// payload. A nil v yields a response with no data field, used for commands that
// return nothing (remove_profile).
func newResult(id int64, v any) (Response, error) {
	if v == nil {
		return Response{ID: id, Ok: true}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return Response{}, fmt.Errorf("control: marshal response data: %w", err)
	}
	return Response{ID: id, Ok: true, Data: raw}, nil
}

// newError builds a failed Response for id carrying msg.
func newError(id int64, msg string) Response {
	return Response{ID: id, Ok: false, Error: msg}
}

// marshalLine encodes v as a single JSON line terminated by '\n'. It rejects
// payloads that themselves contain a newline only implicitly — json.Marshal
// never emits a literal newline in compact output, so one object maps to one
// line.
func marshalLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	return b, nil
}

// writeMessage marshals v and writes it as one line to w.
func writeMessage(w io.Writer, v any) error {
	line, err := marshalLine(v)
	if err != nil {
		return err
	}
	_, err = w.Write(line)
	return err
}

// marshalEvent renders an event with name and an already-marshalled body into a
// single JSON object. The body's fields are merged with the event field so the
// wire form is a flat object like {"event":"state","state":"connected"} rather
// than a nested {"event":...,"data":...}.
func marshalEvent(name string, body any) ([]byte, error) {
	bodyRaw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("control: marshal event body: %w", err)
	}
	// Splice {"event":name, <body fields>}. body must marshal to a JSON object;
	// every event body type here does.
	out := make([]byte, 0, len(bodyRaw)+24)
	prefix, err := json.Marshal(struct {
		Event string `json:"event"`
	}{name})
	if err != nil {
		return nil, err
	}
	out = append(out, prefix...)
	if len(bodyRaw) > 2 { // body is not "{}"
		out[len(out)-1] = ',' // replace closing brace of prefix
		out = append(out, bodyRaw[1:]...)
	}
	out = append(out, '\n')
	return out, nil
}

// decodeRequest reads and parses one request line from r. It returns io.EOF when
// the stream ends cleanly between messages.
func decodeRequest(line []byte) (Request, error) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Request{}, fmt.Errorf("control: bad request json: %w", err)
	}
	return req, nil
}

// requestScanner reads newline-delimited request lines from an io.Reader. It
// wraps bufio.Scanner with a larger buffer so long subscription links don't
// overflow the default 64 KiB token limit.
type requestScanner struct {
	sc *bufio.Scanner
}

func newRequestScanner(r io.Reader) *requestScanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &requestScanner{sc: sc}
}

// next returns the next non-empty line, true, or nil/false at end of stream.
// Blank lines are skipped so a stray newline isn't a parse error.
func (s *requestScanner) next() ([]byte, bool) {
	for s.sc.Scan() {
		line := bytes.TrimSpace(s.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		// Copy: Scanner reuses its buffer on the next Scan.
		out := make([]byte, len(line))
		copy(out, line)
		return out, true
	}
	return nil, false
}

// err reports any scanning error other than a clean EOF.
func (s *requestScanner) err() error { return s.sc.Err() }
