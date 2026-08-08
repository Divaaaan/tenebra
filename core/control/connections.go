package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// maxLiveConnections caps the live-host list. This answer is a menu a person
// picks a site from, not a packet capture: past a couple of hundred rows nobody
// reads further, while a busy machine (a torrent client, a browser with a
// hundred tabs) can hold thousands of sockets, and serialising all of them would
// cost real time on both ends for rows that scroll past unseen. The cap keeps
// the busiest hosts, which are the ones worth a routing rule.
const maxLiveConnections = 200

// LiveConnection is one row of the live-host list: every current connection to
// the same host folded into a single entry. It is what a user clicks to write a
// routing rule, so Host is the value that would go into that rule verbatim.
type LiveConnection struct {
	// Host is the domain traffic is going to, lowercased. When sniffing never
	// learned a name it is the destination address instead and IsIP is set.
	Host string `json:"host"`
	// Process names the executables behind the connections, busiest first and
	// comma-separated when several share the host (a browser and its updater both
	// talking to the same CDN is ordinary). Empty when the platform did not
	// attribute the connection to a process.
	Process string `json:"process,omitempty"`
	// Up and Down are the summed byte counts of the folded connections.
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
	// Outbound is the sing-box outbound the host's heaviest connection took
	// ("proxy", "direct", ...) — it answers "is this going through the tunnel
	// right now", which is the question the rule is about to change.
	Outbound string `json:"outbound,omitempty"`
	// IsIP marks a row whose Host is an address, not a name. The UI has to show
	// that honestly: a rule written from an address covers that one address, and
	// the site behind it may answer from a dozen more tomorrow.
	IsIP bool `json:"is_ip,omitempty"`
}

// connectionsResult is the data payload of list_connections. The list is always
// present, empty rather than absent, so the UI has one shape to render.
type connectionsResult struct {
	Connections []LiveConnection `json:"connections"`
}

// clashConnections is the subset of the clash API /connections payload the live
// host list reads. The same endpoint feeds Runner.Stats, which distils only the
// two byte totals from it; here we want the per-connection detail.
type clashConnections struct {
	Connections []clashConnection `json:"connections"`
}

// clashConnection is one live socket as the clash API describes it. Chains lists
// the outbounds the connection was routed through, innermost first, so its head
// is the outbound that actually carried the bytes.
type clashConnection struct {
	Metadata clashMetadata `json:"metadata"`
	Upload   int64         `json:"upload"`
	Download int64         `json:"download"`
	Chains   []string      `json:"chains"`
}

// clashMetadata is the addressing half of a connection. Host is filled by the
// sniff rule the route block puts first (see routing.RouteRules), so TLS and
// HTTP connections carry the real domain; anything unsniffable leaves it empty
// and only the address is known.
type clashMetadata struct {
	Host          string `json:"host"`
	DestinationIP string `json:"destinationIP"`
	Process       string `json:"process"`
	ProcessPath   string `json:"processPath"`
}

// handleListConnections answers list_connections with the hosts traffic is
// going to right now, so the user can send one past the tunnel by clicking it
// instead of typing a domain they have to know in advance.
func (d *Daemon) handleListConnections(ctx context.Context, req Request) Response {
	rows, err := d.liveConnections(ctx)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	resp, err := newResult(req.ID, connectionsResult{Connections: rows})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// liveConnections fetches and folds the current connection list. The clash API
// only exists while sing-box runs, so an idle daemon is refused before the
// runner is touched: that is not a failure to report as a broken API but the
// plain fact that there is nothing to list yet.
func (d *Daemon) liveConnections(ctx context.Context) ([]LiveConnection, error) {
	if st := d.snapshotState(); st.State != StateConnected {
		return nil, errors.New("live connections require an active connection")
	}
	body, err := d.runner.ConnectionsJSON(ctx)
	if err != nil {
		// The runner's error names the endpoint and the transport failure, never
		// the token it authenticated with, so it is safe to hand to the UI.
		return nil, fmt.Errorf("live connections unavailable: %w", err)
	}
	return aggregateConnections(body)
}

// aggregateConnections folds a clash /connections payload into one row per host,
// heaviest first. It is split from the fetch so the folding — which is all of the
// behaviour worth pinning down — is testable without an HTTP round trip.
func aggregateConnections(body []byte) ([]LiveConnection, error) {
	var payload clashConnections
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse live connections: %w", err)
	}

	byHost := make(map[string]*hostTraffic, len(payload.Connections))
	for _, c := range payload.Connections {
		host, isIP := connectionHost(c.Metadata)
		if host == "" {
			// Neither a name nor an address: nothing to show and nothing a rule
			// could be written from.
			continue
		}
		agg := byHost[host]
		if agg == nil {
			agg = &hostTraffic{host: host, isIP: isIP}
			byHost[host] = agg
		}
		agg.up += c.Upload
		agg.down += c.Download
		total := c.Upload + c.Download
		if p := processName(c.Metadata); p != "" {
			agg.processes = addWeight(agg.processes, p, total)
		}
		if len(c.Chains) > 0 && c.Chains[0] != "" {
			// The chain lists the outbounds the connection passed through with the
			// one that carried the bytes at its head; the tail is the group it was
			// selected from, which is not what the user is about to reroute.
			agg.outbounds = addWeight(agg.outbounds, c.Chains[0], total)
		}
	}

	rows := make([]LiveConnection, 0, len(byHost))
	for _, agg := range byHost {
		rows = append(rows, agg.row())
	}
	// Busiest host first. The map above has no order of its own and two hosts can
	// carry the same bytes, so the host name breaks ties and the same input always
	// produces the same list.
	sort.Slice(rows, func(i, j int) bool {
		ti, tj := rows[i].Up+rows[i].Down, rows[j].Up+rows[j].Down
		if ti != tj {
			return ti > tj
		}
		return rows[i].Host < rows[j].Host
	})
	if len(rows) > maxLiveConnections {
		rows = rows[:maxLiveConnections]
	}
	return rows, nil
}

// hostTraffic accumulates every connection to one host. Processes and outbounds
// are kept with their byte weight rather than as bare sets so the busiest one
// can be named first — which process is doing the talking, and whether the bulk
// of the bytes is tunnelled, are the two things worth reporting when several
// disagree.
type hostTraffic struct {
	host      string
	isIP      bool
	up, down  int64
	processes []weighted
	outbounds []weighted
}

// weighted is a name with the byte count attributed to it.
type weighted struct {
	name  string
	bytes int64
}

// addWeight credits bytes to name in a weight list, appending it on first sight.
// The lists hold a handful of entries at most (the processes talking to one
// host), so a linear scan beats a map and costs nothing to keep in order.
func addWeight(list []weighted, name string, bytes int64) []weighted {
	for i := range list {
		if list[i].name == name {
			list[i].bytes += bytes
			return list
		}
	}
	return append(list, weighted{name: name, bytes: bytes})
}

// row renders the aggregate as the UI row.
func (h *hostTraffic) row() LiveConnection {
	row := LiveConnection{
		Host:    h.host,
		Process: strings.Join(rankNames(h.processes), ", "),
		Up:      h.up,
		Down:    h.down,
		IsIP:    h.isIP,
	}
	if out := rankNames(h.outbounds); len(out) > 0 {
		row.Outbound = out[0]
	}
	return row
}

// rankNames orders weighted names by bytes, heaviest first, with the name as the
// tie-break so the result never depends on the order the API listed connections
// in.
func rankNames(list []weighted) []string {
	sorted := make([]weighted, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].bytes != sorted[j].bytes {
			return sorted[i].bytes > sorted[j].bytes
		}
		return sorted[i].name < sorted[j].name
	})
	names := make([]string, len(sorted))
	for i, w := range sorted {
		names[i] = w.name
	}
	return names
}

// connectionHost picks what the row is keyed and labelled by: the sniffed domain
// when there is one, else the destination address. The address flag is decided
// by parsing the value rather than by which field it came from — sing-box puts a
// literal address in the host field when a client dialled one directly, and that
// row deserves the same warning as a row that fell back to the address.
func connectionHost(m clashMetadata) (host string, isIP bool) {
	h := strings.TrimSpace(m.Host)
	if h == "" {
		h = strings.TrimSpace(m.DestinationIP)
	}
	if h == "" {
		return "", false
	}
	// Domains are case-insensitive, so fold them: without this the same site
	// appears twice whenever an app spells it differently.
	h = strings.ToLower(h)
	_, err := netip.ParseAddr(h)
	return h, err == nil
}

// processName is the executable behind a connection. sing-box reports the bare
// name when it can and the full path otherwise.
func processName(m clashMetadata) string {
	if p := strings.TrimSpace(m.Process); p != "" {
		return p
	}
	return baseName(strings.TrimSpace(m.ProcessPath))
}

// baseName cuts a process path down to its file name. It splits on both
// separators instead of using filepath.Base because the path describes the
// machine the traffic is on, not the machine this code was built for: a Windows
// path arrives with backslashes even in a test binary compiled for Linux.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
