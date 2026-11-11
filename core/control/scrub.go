package control

import "github.com/Divaaaan/tenebra/core/redact"

// Secret masking for free-form diagnostic text.
//
// The struct-level redaction elsewhere in this package (redactProfile) keeps
// credentials out of the protocol's typed replies. This is the other half: a
// diagnostics bundle is assembled from log lines and error strings, where a
// subscription token or a node UUID can turn up in text nobody designed. The
// bundle exists to be pasted into a chat with a stranger, so it is scrubbed on
// the way out.
//
// The patterns mirror ui-desktop/src/lib/diagnostics.ts one for one, so the
// bundle the core produces and the one the app copies from its log console mask
// the same things. Deliberately light: it targets the few shapes that actually
// carry a secret and leaves protocols, hosts, ports and error text readable,
// because those are the entire point of a diagnostics dump.

// scrubSecrets masks subscription tokens and node credentials in a block of
// text, leaving everything else readable.
func scrubSecrets(text string) string {
	return redact.Text(text)
}
