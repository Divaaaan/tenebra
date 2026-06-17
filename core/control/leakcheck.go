package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/profile"
)

// LeakCheck is the result of the leak_check command. It reports the observed
// public IP (with a best-effort country), a verdict on whether traffic is
// leaving through the active tunnel exit, and a best-effort DNS assessment.
//
// The type is deliberately conservative: it never claims "no leak" beyond what
// was actually measured. When a check cannot be made robustly it says so
// (ExitMatch "unknown", a DNS status of "inconclusive"/"unavailable") rather
// than reporting a false pass.
type LeakCheck struct {
	// PublicIP is the public IP observed from the machine's current vantage
	// point. Empty if every echo endpoint failed (then IPVerdict is "error").
	PublicIP string `json:"public_ip,omitempty"`
	// Country is a best-effort ISO 3166-1 alpha-2 country code for PublicIP,
	// filled only when an echo endpoint returns it cheaply. Empty otherwise.
	Country string `json:"country,omitempty"`
	// Source is the echo endpoint that answered, for transparency about where
	// the IP came from. Empty when no endpoint answered.
	Source string `json:"source,omitempty"`

	// Connected reports whether a tunnel was active at check time. It decides
	// how PublicIP is judged: against the exit when connected, neutrally when
	// not.
	Connected bool `json:"connected"`
	// ExitServer is the active node's configured server address (host or IP),
	// present only when connected. It is what PublicIP is compared against.
	ExitServer string `json:"exit_server,omitempty"`
	// ExitMatch is the verdict on whether the observed IP corresponds to the
	// tunnel exit: "match" (the IP is the exit, traffic is tunnelled),
	// "mismatch" (the IP is clearly not the exit — a likely leak), or "unknown"
	// (could not be determined, e.g. the exit is a hostname we did not resolve,
	// or no IP was observed). Empty when not connected.
	ExitMatch ExitVerdict `json:"exit_match,omitempty"`
	// IPVerdict is the overall severity of the IP finding for the UI to style:
	// "ok" (tunnelled or simply reporting the IP when idle), "warn" (a probable
	// leak — connected but the IP is not the exit), "neutral" (idle, IP shown
	// without a pass/fail claim), or "error" (no IP could be observed).
	IPVerdict Verdict `json:"ip_verdict"`
	// IPMessage is a short human summary of the IP finding.
	IPMessage string `json:"ip_message"`

	// DNS is the best-effort DNS leak assessment. It is honest about its limits:
	// a full dnsleaktest-style flow is out of scope, so an undeterminable result
	// is reported as inconclusive/unavailable, never as a pass.
	DNS DNSResult `json:"dns"`
}

// ExitVerdict is the IP-vs-exit comparison outcome.
type ExitVerdict string

const (
	ExitMatchMatch    ExitVerdict = "match"
	ExitMatchMismatch ExitVerdict = "mismatch"
	ExitMatchUnknown  ExitVerdict = "unknown"
)

// Verdict is a severity level the UI maps to pass/warn styling.
type Verdict string

const (
	VerdictOK      Verdict = "ok"
	VerdictWarn    Verdict = "warn"
	VerdictNeutral Verdict = "neutral"
	VerdictError   Verdict = "error"
)

// DNSStatus is the DNS leak assessment outcome.
type DNSStatus string

const (
	// DNSOK means the observed resolver(s) look consistent with the tunnel
	// (best-effort; only set when we could reason about them).
	DNSOK DNSStatus = "ok"
	// DNSLeak means the resolver(s) look like they bypass the tunnel.
	DNSLeak DNSStatus = "leak"
	// DNSInconclusive means we got some signal but cannot make a confident
	// pass/fail call. Never treat this as safe.
	DNSInconclusive DNSStatus = "inconclusive"
	// DNSUnavailable means the DNS probe itself could not run (endpoint
	// unreachable, etc.). Never treat this as safe.
	DNSUnavailable DNSStatus = "unavailable"
)

// DNSResult is the DNS portion of a leak check.
type DNSResult struct {
	Status DNSStatus `json:"status"`
	// Resolvers are the resolver IPs we could observe, if any. Best-effort and
	// may be empty even on a successful probe.
	Resolvers []string `json:"resolvers,omitempty"`
	Message   string   `json:"message"`
}

// ipEcho is a public, neutral echo service that reports the caller's public IP.
// Parse selects how to read its response body. The list is tried in order and
// the first success wins, so a single endpoint being down or blocked does not
// fail the check.
type ipEcho struct {
	name  string
	url   string
	parse func([]byte) (ip, country string)
}

// defaultIPEchoes are public IP-echo endpoints used in production. They are
// neutral third-party services (no project infrastructure). Tests never use
// these — they inject canned responses through the httpGet field.
var defaultIPEchoes = []ipEcho{
	{name: "ipify", url: "https://api.ipify.org?format=json", parse: parseIPJSON},
	{name: "ifconfig.co", url: "https://ifconfig.co/json", parse: parseIfconfigJSON},
	{name: "icanhazip", url: "https://icanhazip.com", parse: parseIPText},
}

// dnsEchoURL is a public resolver-echo endpoint: the HTTP request is answered by
// a host whose name must be resolved first, and the JSON reports the resolver IP
// that did the lookup. It is best-effort; if it is unreachable the DNS result is
// "unavailable", never a fabricated pass.
const dnsEchoURL = "https://edns.ip-api.com/json"

// leakHTTPTimeout bounds a single echo request. The whole check fans these out
// with its own per-endpoint context derived from the command context.
const leakHTTPTimeout = 8 * time.Second

// defaultHTTPGet is the production HTTP getter: a plain GET with a browser-like
// UA and a bounded body read. It is injected into the daemon so leak_check can
// be unit-tested offline.
func defaultHTTPGet(ctx context.Context, url string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("leakcheck: get %s: %w", url, err)
	}
	req.Header.Set("User-Agent", leakUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	client := &http.Client{Timeout: leakHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("leakcheck: get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("leakcheck: get %s: unexpected status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, leakMaxBody))
	if err != nil {
		return nil, fmt.Errorf("leakcheck: get %s: read body: %w", url, err)
	}
	return body, nil
}

// leakUserAgent is a neutral browser-like UA; some echo services vary their
// output (text vs JSON) on it, so we keep it stable.
const leakUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// leakMaxBody caps an echo response; these payloads are tiny, so a small cap is
// plenty and bounds memory against a misbehaving endpoint.
const leakMaxBody = 64 << 10

// handleLeakCheck runs the IP/DNS leak check and returns its verdict.
func (d *Daemon) handleLeakCheck(ctx context.Context, req Request) Response {
	res := d.runLeakCheck(ctx)
	resp, err := newResult(req.ID, res)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// runLeakCheck performs the IP and DNS checks and assembles the verdict. It
// snapshots the connection state, looks up the active exit if connected, fetches
// the public IP from redundant endpoints, judges the exit match, and runs the
// best-effort DNS probe. All HTTP goes through d.httpGet so tests run offline.
func (d *Daemon) runLeakCheck(ctx context.Context) LeakCheck {
	st := d.snapshotState()
	connected := st.State == StateConnected

	var res LeakCheck
	res.Connected = connected
	if connected {
		res.ExitServer = d.exitServer(st)
	}

	ip, country, source := d.observePublicIP(ctx)
	res.PublicIP = ip
	res.Country = country
	res.Source = source

	d.judgeIP(&res, connected)
	res.DNS = d.observeDNS(ctx)
	return res
}

// exitServer returns the configured server address (host or IP) of the node the
// state says is connected, by looking it up in its profile. Empty if the node or
// profile is no longer known. This is what the observed public IP is compared
// against for the exit-match verdict.
func (d *Daemon) exitServer(st State) string {
	if st.Profile == "" || st.Node == "" {
		return ""
	}
	p, ok := d.store.Get(st.Profile)
	if !ok {
		return ""
	}
	for _, s := range p.Servers {
		if s.ID == st.Node {
			return serverAddress(s)
		}
	}
	return ""
}

// serverAddress is the host/IP a node dials. For most protocols that is
// Node.Server; that field is the exit endpoint we expect the public IP to match
// when the tunnel carries traffic.
func serverAddress(s profile.Server) string {
	return strings.TrimSpace(s.Server)
}

// observePublicIP tries each echo endpoint in order and returns the first IP it
// can parse, along with any country the endpoint volunteered and the endpoint's
// name. It returns empty strings if every endpoint fails. Each request gets its
// own short timeout derived from ctx so one slow endpoint does not stall the
// fallback.
func (d *Daemon) observePublicIP(ctx context.Context) (ip, country, source string) {
	for _, echo := range d.ipEchoes {
		reqCtx, cancel := context.WithTimeout(ctx, leakHTTPTimeout)
		body, err := d.httpGet(reqCtx, echo.url)
		cancel()
		if err != nil {
			d.emitLog(LogWarn, fmt.Sprintf("leak-check: %s unreachable: %v", echo.name, err))
			continue
		}
		gotIP, gotCountry := echo.parse(body)
		gotIP = strings.TrimSpace(gotIP)
		if net.ParseIP(gotIP) == nil {
			d.emitLog(LogWarn, fmt.Sprintf("leak-check: %s returned no usable IP", echo.name))
			continue
		}
		return gotIP, strings.TrimSpace(gotCountry), echo.name
	}
	return "", "", ""
}

// judgeIP fills the IP verdict fields from the observed IP and the connection
// state. The honesty rule is central here: connected with a matching exit is the
// only "ok" while connected; connected with a clearly different IP is a "warn"
// (a probable leak); an exit we cannot compare yields "unknown"/"ok"-by-presence
// is never asserted. Idle reports the IP with a neutral verdict and no pass/fail
// claim.
func (d *Daemon) judgeIP(res *LeakCheck, connected bool) {
	if res.PublicIP == "" {
		res.IPVerdict = VerdictError
		res.IPMessage = "Could not determine the public IP from any echo service."
		if connected {
			res.ExitMatch = ExitMatchUnknown
		}
		return
	}

	if !connected {
		res.IPVerdict = VerdictNeutral
		res.IPMessage = fmt.Sprintf("Not connected. Current public IP is %s.", res.PublicIP)
		return
	}

	switch matchExit(res.PublicIP, res.ExitServer) {
	case ExitMatchMatch:
		res.ExitMatch = ExitMatchMatch
		res.IPVerdict = VerdictOK
		res.IPMessage = fmt.Sprintf("Public IP %s matches the tunnel exit.", res.PublicIP)
	case ExitMatchMismatch:
		res.ExitMatch = ExitMatchMismatch
		res.IPVerdict = VerdictWarn
		res.IPMessage = fmt.Sprintf("Public IP %s does not match the tunnel exit %s — traffic may not be going through the tunnel.", res.PublicIP, res.ExitServer)
	default:
		res.ExitMatch = ExitMatchUnknown
		res.IPVerdict = VerdictNeutral
		if res.ExitServer == "" {
			res.IPMessage = fmt.Sprintf("Connected. Public IP is %s; the exit address is unknown, so a match could not be verified.", res.PublicIP)
		} else {
			res.IPMessage = fmt.Sprintf("Connected. Public IP is %s; the exit %s is a hostname that was not resolved, so a match could not be verified.", res.PublicIP, res.ExitServer)
		}
	}
}

// matchExit compares an observed public IP against a node's configured exit
// address. When the exit is a literal IP the comparison is exact. When the exit
// is a hostname we do not resolve it here (resolution would itself go through the
// host resolver and muddy the result), so we return "unknown" rather than guess.
func matchExit(publicIP, exit string) ExitVerdict {
	exit = strings.TrimSpace(exit)
	if exit == "" {
		return ExitMatchUnknown
	}
	// A bracketed or host:port form should not occur for Node.Server, but strip a
	// trailing port defensively if one slipped in.
	if host, _, err := net.SplitHostPort(exit); err == nil {
		exit = host
	}
	exitIP := net.ParseIP(exit)
	if exitIP == nil {
		// Exit is a hostname; we cannot confidently equate it to the observed IP
		// without resolving, which we intentionally avoid.
		return ExitMatchUnknown
	}
	obs := net.ParseIP(publicIP)
	if obs == nil {
		return ExitMatchUnknown
	}
	if obs.Equal(exitIP) {
		return ExitMatchMatch
	}
	return ExitMatchMismatch
}

// observeDNS runs the best-effort DNS probe. It queries a resolver-echo endpoint
// (whose hostname must be resolved first) and reports the resolver IP it sees.
// It is deliberately conservative: any failure to determine the resolver yields
// "unavailable", and a determined resolver yields "inconclusive" with the
// observed addresses rather than a confident pass — distinguishing a tunnelled
// resolver from a leaking one robustly is out of scope without a dnsleaktest-
// style flow, and we will not fabricate a pass.
func (d *Daemon) observeDNS(ctx context.Context) DNSResult {
	reqCtx, cancel := context.WithTimeout(ctx, leakHTTPTimeout)
	body, err := d.httpGet(reqCtx, d.dnsEchoURL)
	cancel()
	if err != nil {
		return DNSResult{
			Status:  DNSUnavailable,
			Message: "DNS resolver could not be determined (the probe endpoint was unreachable). This is not a pass.",
		}
	}

	resolvers := parseDNSResolvers(body)
	if len(resolvers) == 0 {
		return DNSResult{
			Status:  DNSInconclusive,
			Message: "The DNS probe responded but did not reveal a resolver address, so a DNS leak could not be assessed.",
		}
	}
	return DNSResult{
		Status:    DNSInconclusive,
		Resolvers: resolvers,
		Message:   "Observed resolver(s) shown. A reliable leak/no-leak verdict needs a full DNS leak test, which is out of scope, so this is reported as inconclusive rather than a pass.",
	}
}

// parseIPText reads a bare-IP response body (one IP, possibly trailing newline),
// as returned by icanhazip-style endpoints. No country is available.
func parseIPText(body []byte) (ip, country string) {
	return strings.TrimSpace(string(body)), ""
}

// parseIPJSON reads {"ip":"..."} as returned by ipify's JSON mode. No country.
func parseIPJSON(body []byte) (ip, country string) {
	var v struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", ""
	}
	return v.IP, ""
}

// parseIfconfigJSON reads ifconfig.co's JSON, which carries both the IP and a
// country code, so a connected check can show the exit country cheaply.
func parseIfconfigJSON(body []byte) (ip, country string) {
	var v struct {
		IP          string `json:"ip"`
		Country     string `json:"country"`
		CountryISO  string `json:"country_iso"`
		CountryCode string `json:"country_code"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", ""
	}
	c := firstNonEmpty(v.CountryISO, v.CountryCode, v.Country)
	return v.IP, c
}

// parseDNSResolvers reads the resolver-echo JSON and extracts the resolver IP(s)
// it reports. The shape varies across endpoints, so we accept a few common
// fields and keep only values that parse as IPs. Returns nil if none are found.
func parseDNSResolvers(body []byte) []string {
	var v struct {
		DNS struct {
			IP string `json:"ip"`
		} `json:"dns"`
		EDNS struct {
			IP string `json:"ip"`
		} `json:"edns"`
		Resolver  string   `json:"resolver"`
		Resolvers []string `json:"resolvers"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	candidates := []string{v.DNS.IP, v.EDNS.IP, v.Resolver}
	candidates = append(candidates, v.Resolvers...)

	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if net.ParseIP(c) == nil {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// firstNonEmpty returns the first non-empty, trimmed string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
