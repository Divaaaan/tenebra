package dpi

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// maxStrategyArgs and maxArgLen bound how much a strategy can hand to the
// spawned process. Real strategies are a handful of short tokens; anything
// beyond these is either a mistake or an attempt to find a limit.
const (
	maxStrategyArgs = 32
	maxArgLen       = 128
)

// The value shapes an option's argument may take. They are deliberately tight:
// every one of these strings ends up in the argv of a process we spawn, so the
// grammar of a desync parameter is the whole vocabulary, and a value that does
// not fit it is refused rather than trimmed.
var (
	// posValue matches ByeDPI's position format, "offset[:repeats:skip][+flags]"
	// (e.g. "1", "3+s", "2:5:10", "-1"): a byte offset that may be negative,
	// optionally repeated, optionally anchored to a landmark in the payload —
	// +s SNI, +h HTTP host, +n null, +e end, +m middle.
	posValue = regexp.MustCompile(`^-?\d{1,5}(:\d{1,5}){0,2}(\+[shnem]{1,5})?$`)

	// numValue matches a plain count: TTL, buffer size, timeout, log level.
	numValue = regexp.MustCompile(`^\d{1,5}$`)

	// rangeValue matches a single number or an inclusive range, "n" or "n-m",
	// as --round and --pf take.
	rangeValue = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?$`)

	// listValue matches the comma-separated keyword lists ByeDPI uses for
	// --auto (torst,redirect,ssl_err,none), --mod-http (h,d,r), --proto
	// (t,h,u,i) and --fake-tls-mod (rand,orig,msize=1200).
	listValue = regexp.MustCompile(`^[a-z0-9_]{1,16}(=\d{1,5})?(,[a-z0-9_]{1,16}(=\d{1,5})?){0,7}$`)

	// hostValue matches the server name planted in a fake ClientHello: a
	// hostname, plus ByeDPI's randomisation placeholders (? # *).
	hostValue = regexp.MustCompile(`^[A-Za-z0-9._*#?-]{1,253}$`)

	// charValue matches one printable ASCII byte, which is all --oob-data takes.
	charValue = regexp.MustCompile(`^[\x21-\x7e]$`)
)

// flagSpec describes one accepted option: value is the shape its argument must
// match, or nil when the option takes no argument.
type flagSpec struct {
	value *regexp.Regexp
}

// desyncFlags is the complete set of ByeDPI options a strategy may use, listed
// once with both spellings ciadpi accepts. Everything here only shapes how the
// opening packets of a forwarded connection are cut, faked or reordered: none
// of it touches the filesystem, moves the listener, or changes who may connect.
// Options outside this table are refused, so a new ByeDPI release widening the
// option set does not silently widen ours.
var desyncFlags = []struct {
	long, short string
	value       *regexp.Regexp
}{
	// Where and how the first packets are cut.
	{"--split", "-s", posValue},
	{"--disorder", "-d", posValue},
	{"--oob", "-o", posValue},
	{"--disoob", "-q", posValue},
	{"--fake", "-f", posValue},
	{"--fake-offset", "-O", posValue},
	{"--tlsrec", "-r", posValue},
	// What the fake packets look like.
	{"--fake-sni", "-n", hostValue},
	{"--fake-tls-mod", "-Q", listValue},
	{"--oob-data", "-e", charValue},
	{"--ttl", "-t", numValue},
	{"--def-ttl", "-g", numValue},
	{"--tlsminor", "-m", numValue},
	{"--udp-fake", "-a", numValue},
	{"--mod-http", "-M", listValue},
	// When a desync applies, and what the engine retries on failure.
	{"--auto", "-A", listValue},
	{"--auto-mode", "-L", numValue},
	{"--cache-ttl", "-u", numValue},
	{"--timeout", "-T", numValue},
	{"--round", "-R", rangeValue},
	{"--proto", "-K", listValue},
	{"--pf", "-V", rangeValue},
	// Engine limits and diagnostics.
	{"--max-conn", "-c", numValue},
	{"--buf-size", "-b", numValue},
	{"--debug", "-x", numValue},
	{"--no-domain", "-N", nil},
	{"--no-udp", "-U", nil},
}

// The reasons an option ciadpi understands is still kept away from strategies.
// They are spelled out so a refusal tells the user what the rule is instead of
// looking like an unsupported-option typo.
const (
	listenerReason = "the listener address is set by tenebra, and binding it off loopback would publish an open proxy"
	bindReason     = "the outgoing bind address is decided by routing, not by a strategy"
	fileReason     = "it makes the bypass process read or write a file of the caller's choosing"
)

// managedFlags are the options a strategy must not carry, keyed by every
// spelling, with the reason each is withheld.
var managedFlags = map[string]string{
	"-i": listenerReason, "--ip": listenerReason,
	"-p": listenerReason, "--port": listenerReason,
	"-I": bindReason, "--conn-ip": bindReason,
	"-H": fileReason, "--hosts": fileReason,
	"-j": fileReason, "--ipset": fileReason,
	"-l": fileReason, "--fake-data": fileReason,
	"-y": fileReason, "--cache-dump": fileReason,
}

// allowedFlags indexes desyncFlags by every spelling of every option.
var allowedFlags = buildFlagIndex()

func buildFlagIndex() map[string]flagSpec {
	index := make(map[string]flagSpec, 2*len(desyncFlags))
	for _, f := range desyncFlags {
		index[f.long] = flagSpec{value: f.value}
		if f.short != "" {
			index[f.short] = flagSpec{value: f.value}
		}
	}
	return index
}

// BuildArgs renders the full ByeDPI command line: the loopback listener this
// build owns, followed by the strategy's validated options.
//
// host must be an IP literal on loopback. The bypass proxy authenticates
// nobody and forwards whatever reaches it, so a listener on a routable address
// would be an open proxy for the whole network — that is a property worth
// enforcing where the command line is built rather than trusting every caller
// to pass the right constant.
func BuildArgs(host string, port int, strategyArgs []string) ([]string, error) {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, fmt.Errorf("dpi: listen address %q is not an IP literal: %w", host, err)
	}
	if !addr.IsLoopback() {
		return nil, fmt.Errorf("dpi: listen address %s is not on loopback", addr)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("dpi: listen port %d is out of range", port)
	}

	rest, err := ValidateArgs(strategyArgs)
	if err != nil {
		return nil, err
	}

	argv := make([]string, 0, 4+len(rest))
	argv = append(argv, "-i", addr.String(), "-p", strconv.Itoa(port))
	return append(argv, rest...), nil
}

// ValidateArgs checks a strategy's options against the allowlist above and
// returns a copy safe to hand to exec.
//
// This is a security boundary, not a convenience check. Strategy options are
// the one part of the bypass a user, a subscription or a future auto-tuner can
// influence, and they land verbatim in the argv of a process this build spawns.
// ByeDPI's own option set includes reading and writing files (--hosts, --ipset,
// --fake-data, --cache-dump) and moving the listener (--ip, --port); either
// turns a strategy string into something the user never asked for — a file
// written wherever the service can write, or a proxy open to the network. So
// only the desync-shaping options are accepted, each with a value shape it has
// to match, and anything else is refused by name so the failure is legible
// rather than silently dropped.
//
// Tokens come back exactly as written rather than normalised: ciadpi takes both
// "--auto=torst" and "--auto torst", and the shipped presets were verified on
// the wire in the spelling they carry.
func ValidateArgs(args []string) ([]string, error) {
	if len(args) > maxStrategyArgs {
		return nil, fmt.Errorf("dpi: strategy has %d arguments, at most %d allowed", len(args), maxStrategyArgs)
	}
	for i, a := range args {
		if a == "" {
			return nil, fmt.Errorf("dpi: argument %d is empty", i)
		}
		if len(a) > maxArgLen {
			return nil, fmt.Errorf("dpi: argument %d is %d bytes, at most %d allowed", i, len(a), maxArgLen)
		}
	}

	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		token := args[i]
		name, inline, hasInline := strings.Cut(token, "=")
		if !strings.HasPrefix(name, "-") || name == "-" || name == "--" {
			return nil, fmt.Errorf("dpi: expected a ByeDPI option, got %q", token)
		}
		if reason, managed := managedFlags[name]; managed {
			return nil, fmt.Errorf("dpi: option %s is not available to a strategy: %s", name, reason)
		}
		spec, ok := allowedFlags[name]
		if !ok {
			return nil, unknownOption(name)
		}
		// Short options take their value as a separate token; ciadpi's getopt
		// would read "-s=1" as the value "=1", which is never what was meant.
		if hasInline && !strings.HasPrefix(name, "--") {
			return nil, fmt.Errorf("dpi: option %s takes its value as a separate argument, not %q", name, token)
		}

		if spec.value == nil {
			if hasInline {
				return nil, fmt.Errorf("dpi: option %s takes no value", name)
			}
			out = append(out, token)
			continue
		}

		value := inline
		if !hasInline {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("dpi: option %s is missing its value", name)
			}
			i++
			value = args[i]
		}
		if !spec.value.MatchString(value) {
			return nil, fmt.Errorf("dpi: invalid value %q for option %s", value, name)
		}

		out = append(out, token)
		if !hasInline {
			out = append(out, value)
		}
	}
	return out, nil
}

// unknownOption explains a name that is in neither table. A short option with
// its value glued on ("-s1+s", the spelling ByeDPI's own docs use) is the
// likely mistake, so it is called out instead of reported as unsupported.
func unknownOption(name string) error {
	if len(name) > 2 && !strings.HasPrefix(name, "--") {
		short, value := name[:2], name[2:]
		if reason, managed := managedFlags[short]; managed {
			return fmt.Errorf("dpi: option %s is not available to a strategy: %s", short, reason)
		}
		if _, known := allowedFlags[short]; known {
			return fmt.Errorf("dpi: option %s takes its value as a separate argument, write %q", short, short+" "+value)
		}
	}
	return fmt.Errorf("dpi: unsupported ByeDPI option %q", name)
}
