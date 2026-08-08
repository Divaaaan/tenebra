package dpi

import (
	"errors"
	"fmt"
	"slices"
)

// A Strategy is one named way of reshaping the opening packets of a forwarded
// connection: the set of ByeDPI options the bypass process runs with. It is the
// unit a user picks, the daemon persists, and a future auto-tuner would walk.
//
// A strategy changes nothing about WHERE traffic goes — the same destination,
// over the same direct leg, from the same address. It only changes how the
// first packets of each connection look on the wire, which is the axis a
// handshake-shape filter keys on. That scoping is what makes trying another one
// cheap: a strategy that does not help simply fails to get through, exactly as
// the previous one did.
type Strategy struct {
	// Name is a short, stable identifier used in settings, logs and the UI.
	Name string
	// Args are the ByeDPI options this strategy runs with, in the order ciadpi
	// reads them. Order carries meaning: options after --auto form the group the
	// engine retries with once it detects the first attempt was blocked.
	Args []string
}

// Validate reports whether the strategy is usable: it must be named, and its
// options must pass the same allowlist anything a user types goes through.
func (s Strategy) Validate() error {
	if s.Name == "" {
		return errors.New("dpi: strategy has no name")
	}
	if _, err := ValidateArgs(s.Args); err != nil {
		return fmt.Errorf("dpi: strategy %q: %w", s.Name, err)
	}
	return nil
}

// Lookup returns the built-in strategy with the given name. The args are cloned
// so a caller cannot rewrite the shipped preset for the rest of the process.
func Lookup(name string) (Strategy, bool) {
	if name == "" {
		return Strategy{}, false
	}
	for _, s := range DefaultStrategies {
		if s.Name == name {
			return Strategy{Name: s.Name, Args: slices.Clone(s.Args)}, true
		}
	}
	return Strategy{}, false
}

// DefaultStrategies is the set of bypass presets the client ships, ordered so
// the first entry is the one to run when nobody has chosen.
//
// That first entry is the compound preset ByeDPI itself ships as its default
// launcher, and it is the one verified end to end on a live network before this
// code was written: the listener came up, a request through it returned, and a
// 200 KB body arrived whole rather than stalling at the first blocked segment.
// It leads the list because it is the only entry with that evidence behind it —
// it cuts the first record, sends a reordered segment anchored to the SNI,
// mixes the case of HTTP headers, retries a second shape when it sees an
// injected reset, and splits the TLS record too.
//
// The rest are single-technique presets, ordered least to most invasive, for
// the networks where the compound one is too much or simply does not fit: a
// plain split, the same split sent out of order, a TLS record boundary planted
// mid-handshake, and a fake packet with a TTL short enough to die before the
// real server sees it. Simple shapes are the more reliable ones in practice —
// each extra trick is another thing a middlebox can notice, and the elaborate
// ones also cost latency on every connection — so a user who has to move off
// the default is better served walking up this list than down it.
//
// It is a package variable, not a constant, so a build (or a later
// server-informed list) can extend or replace it without touching the engine.
// Every entry is checked against the argv allowlist by the package's tests.
var DefaultStrategies = []Strategy{
	{
		Name: "auto",
		Args: []string{"--split", "1", "--disorder", "3+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"},
	},
	{
		Name: "split-sni",
		Args: []string{"--split", "1+s"},
	},
	{
		Name: "disorder-sni",
		Args: []string{"--disorder", "1+s"},
	},
	{
		Name: "tls-record",
		Args: []string{"--tlsrec", "1+s"},
	},
	{
		Name: "fake-ttl",
		Args: []string{"--fake", "1+s", "--ttl", "8"},
	},
}
