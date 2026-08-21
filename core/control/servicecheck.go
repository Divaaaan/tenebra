package control

import (
	"context"
	"net"
	"sync"
	"time"
)

// ServiceCheck is one answer to "is the thing I installed this for working?".
//
// Named after what the user recognises — video, voice chat, games — rather than
// after the mechanism, because the mechanism is what this app is supposed to
// stop them having to think about.
type ServiceCheck struct {
	// Service is a stable key the UI labels: "video", "voice", "games".
	Service string `json:"service"`
	// OK is whether it worked.
	OK bool `json:"ok"`
	// RTTMs is what it cost, in milliseconds. Meaningful only when OK.
	RTTMs int64 `json:"rttMs"`
	// Detail names the destination that was measured, so a failure can be
	// repeated by hand instead of argued about.
	Detail string `json:"detail"`
}

// serviceCheckTimeout bounds one service probe. Generous enough that a censored
// destination fails by being censored rather than by being slow, short enough
// that three of them in parallel stay inside a few seconds.
const serviceCheckTimeout = 6 * time.Second

// videoProbeURL and voiceProbeHost are what the video and voice checks measure.
//
// YouTube's 204 endpoint is the same one the bypass strategies are scored
// against, so a green check here means the same thing it means there. Discord's
// voice path is probed at its gateway rather than at a voice server: which voice
// server a session gets is assigned per call and cannot be known in advance —
// the addresses moved to Google Cloud (35.217.0.0/16) at some point, which is
// exactly the kind of change a hardcoded probe target would have hidden.
const (
	videoProbeURL  = "https://www.youtube.com/generate_204"
	voiceProbeHost = "gateway.discord.gg:443"
)

// gamesProbeHost is measured on the physical link, not through the tunnel: the
// whole point of the games preset is that game traffic does not pay the tunnel's
// round trip, so the number worth showing is the one games actually get.
const gamesProbeHost = "77.88.8.8:443"

// handleCheckServices answers the only question the user actually has after
// pressing connect: does video work, does voice work, and are games still fast.
//
// The app already reports plenty — exit IP, DNS verdict, throughput, node
// latency — and none of it answers that question. A user watching "connected"
// while YouTube spins has no way to tell whether the tunnel, the bypass, the
// routing or their own network is at fault, and neither has anyone helping them.
// Three named checks turn that into something reportable.
//
// The probes run concurrently: they are independent, and three sequential
// timeouts would take long enough that the UI would have to explain itself.
func (d *Daemon) handleCheckServices(ctx context.Context, req Request) Response {
	checks := make([]ServiceCheck, 3)
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		checks[0] = d.probeVideo(ctx)
	}()
	go func() {
		defer wg.Done()
		checks[1] = d.probeVoice(ctx)
	}()
	go func() {
		defer wg.Done()
		checks[2] = d.probeGames(ctx)
	}()
	wg.Wait()

	out := struct {
		Checks []ServiceCheck `json:"checks"`
	}{Checks: checks}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// probeVideo fetches YouTube's 204 endpoint over whatever path the machine's
// routing gives it — which is the point: it measures what the user's browser
// will get, not what some other path could have got.
func (d *Daemon) probeVideo(ctx context.Context) ServiceCheck {
	ctx, cancel := context.WithTimeout(ctx, serviceCheckTimeout)
	defer cancel()

	start := d.now()
	_, err := d.httpGet(ctx, videoProbeURL)
	if err != nil {
		return ServiceCheck{Service: "video", Detail: videoProbeURL}
	}
	return ServiceCheck{
		Service: "video",
		OK:      true,
		RTTMs:   d.now().Sub(start).Milliseconds(),
		Detail:  videoProbeURL,
	}
}

// probeVoice checks that Discord's gateway is reachable. A working gateway is
// what hands out a voice server, so it failing means voice cannot even be
// attempted; it succeeding does not promise a good call, and the check is
// labelled accordingly rather than pretending to more than it measured.
func (d *Daemon) probeVoice(ctx context.Context) ServiceCheck {
	ctx, cancel := context.WithTimeout(ctx, serviceCheckTimeout)
	defer cancel()

	start := d.now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", voiceProbeHost)
	if err != nil {
		return ServiceCheck{Service: "voice", Detail: voiceProbeHost}
	}
	_ = conn.Close()
	return ServiceCheck{
		Service: "voice",
		OK:      true,
		RTTMs:   d.now().Sub(start).Milliseconds(),
		Detail:  voiceProbeHost,
	}
}

// probeGames measures latency on the physical link, using the same pinned dialer
// the node ping uses (see newPingDialer): with the tunnel up, an ordinary dial
// would be captured by the tun and report the trip to the local device instead of
// the trip games actually make.
func (d *Daemon) probeGames(ctx context.Context) ServiceCheck {
	ctx, cancel := context.WithTimeout(ctx, serviceCheckTimeout)
	defer cancel()

	start := d.now()
	conn, err := d.dial(ctx, "tcp", gamesProbeHost)
	if err != nil {
		return ServiceCheck{Service: "games", Detail: gamesProbeHost}
	}
	_ = conn.Close()
	return ServiceCheck{
		Service: "games",
		OK:      true,
		RTTMs:   d.now().Sub(start).Milliseconds(),
		Detail:  gamesProbeHost,
	}
}
