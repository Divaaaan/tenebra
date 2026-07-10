package subscription

import "strings"

// maxParseDepth caps how deeply the decoder will recurse into nested mappings
// and sequences (both block and flow). The hand-written parser descends one
// stack frame per nesting level, so an adversarial subscription body — e.g.
// several MiB of unmatched '[' or '{' — would otherwise force millions of
// frames and trigger an unrecoverable Go stack overflow, which the auto-refresh
// worker would then hit on every cycle. Real Clash/Mihomo configs nest only a
// handful of levels deep (proxy -> opts -> headers -> value), so 256 is far
// beyond any legitimate document while keeping worst-case stack use tiny. When
// the cap is reached the parser stops descending and returns the partial value
// (an empty collection or the raw scalar), exactly as it already does for any
// shape it does not recognize — a single pathological value must not sink the
// whole import.
const maxParseDepth = 256

// This file implements a deliberately small YAML reader: just enough to pull the
// "proxies" section out of a Clash/Mihomo config. The core ships with no
// third-party dependencies, so rather than take on a general YAML library we
// decode the narrow subset those configs use — a top-level mapping, block and
// flow mappings, block and flow sequences, nested option maps (ws-opts,
// reality-opts, grpc-opts, headers, …) and plain / single-quoted / double-quoted
// / integer / boolean scalars, with '#' comments and blank lines. It is not a
// general-purpose YAML parser and does not try to be: anything it does not
// understand is skipped rather than treated as an error, so an unusual key in an
// unrelated section (rules, dns, proxy-groups) never sinks the proxy list.
//
// Values decode into the usual Go shapes: a mapping is a map[string]any, a
// sequence is a []any, and every scalar is a string (callers coerce to int/bool
// as needed). Keeping scalars as strings avoids a type-guessing step that could
// misread, say, a password that happens to look like a number.

// yamlLine is one significant (non-blank, comment-stripped) line: its
// indentation column, whether it opens a block-sequence item ("- "), the column
// at which the content after a "- " marker begins, and the trimmed content.
type yamlLine struct {
	col        int
	dash       bool
	contentCol int
	text       string
}

// tokenizeYAML splits a document into significant lines, stripping comments and
// blank lines and recording each line's indentation.
func tokenizeYAML(body []byte) []yamlLine {
	var out []yamlLine
	for _, raw := range strings.Split(string(body), "\n") {
		raw = strings.TrimRight(stripYAMLComment(raw), " \t\r")
		col := 0
		for col < len(raw) && (raw[col] == ' ' || raw[col] == '\t') {
			col++
		}
		if col == len(raw) {
			continue // blank or comment-only line
		}
		content := raw[col:]
		if content[0] == '-' && (len(content) == 1 || content[1] == ' ' || content[1] == '\t') {
			// Block-sequence entry. Find where the element's own content starts so
			// a key inline with the dash ("- name: x") aligns its siblings.
			j := 1
			for j < len(content) && (content[j] == ' ' || content[j] == '\t') {
				j++
			}
			out = append(out, yamlLine{col: col, dash: true, contentCol: col + j, text: content[j:]})
			continue
		}
		out = append(out, yamlLine{col: col, dash: false, contentCol: col, text: content})
	}
	return out
}

// stripYAMLComment removes a trailing "# comment". A '#' starts a comment only at
// the start of the content or when preceded by whitespace, and never inside a
// quoted scalar — so a '#' inside a password or a quoted name is preserved.
func stripYAMLComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inDouble:
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == '"' {
				inDouble = false
			}
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '"':
			inDouble = true
		case c == '\'':
			inSingle = true
		case c == '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return s[:i]
			}
		}
	}
	return s
}

// yamlParser walks a tokenized document with a single forward cursor.
type yamlParser struct {
	lines []yamlLine
	pos   int
}

func (p *yamlParser) done() bool    { return p.pos >= len(p.lines) }
func (p *yamlParser) cur() yamlLine { return p.lines[p.pos] }

// decodeYAML parses a whole document into its top-level mapping. It is permissive
// by design and never errors: unrecognized constructs are skipped, and because
// every block is bounded by indentation the parser always resynchronizes at the
// next top-level key.
func decodeYAML(body []byte) map[string]any {
	p := &yamlParser{lines: tokenizeYAML(body)}
	if p.done() {
		return map[string]any{}
	}
	return p.parseMapping(p.cur().col, 0)
}

// parseMapping reads consecutive "key: value" lines at column indent into a map.
// A value that continues on following lines (a nested mapping, or a block
// sequence at the same or a deeper column) is parsed recursively. Lines that are
// not key-shaped are skipped so a stray marker never stalls the parse.
func (p *yamlParser) parseMapping(indent, depth int) map[string]any {
	m := map[string]any{}
	if depth > maxParseDepth {
		// Refuse to descend further. The value is left partial (empty here) and
		// the deeper lines fall through the outer loops' indent checks; this can
		// never spin because the cursor only advances, never rewinds.
		return m
	}
	for !p.done() {
		ln := p.cur()
		if ln.col != indent || ln.dash {
			break
		}
		key, val, ok := splitYAMLKey(ln.text)
		if !ok {
			p.pos++ // not a mapping entry (document marker, alias, …): skip
			continue
		}
		p.pos++
		if val != "" {
			m[key] = parseYAMLInline(val, depth+1)
			continue
		}
		if !p.done() {
			nxt := p.cur()
			// A block sequence may align with its key (col == indent) or be
			// indented deeper; a block mapping must be indented deeper.
			if nxt.dash && nxt.col >= indent {
				m[key] = p.parseSequence(nxt.col, depth+1)
				continue
			}
			if !nxt.dash && nxt.col > indent {
				m[key] = p.parseMapping(nxt.col, depth+1)
				continue
			}
		}
		m[key] = ""
	}
	return m
}

// parseSequence reads consecutive "- " entries at column indent into a slice.
// An entry is a flow value, a scalar, an inline mapping (first key on the dash
// line), or a block value on the following, more-indented lines.
func (p *yamlParser) parseSequence(indent, depth int) []any {
	var out []any
	if depth > maxParseDepth {
		return out // partial (nil) value; see parseMapping's cap note
	}
	for !p.done() {
		ln := p.cur()
		if !ln.dash || ln.col != indent {
			break
		}
		rest := ln.text
		switch {
		case rest == "":
			p.pos++
			if !p.done() && p.cur().col > indent {
				out = append(out, p.parseBlock(p.cur().col, depth+1))
			} else {
				out = append(out, "")
			}
		case rest[0] == '{' || rest[0] == '[':
			p.pos++
			out = append(out, parseYAMLInline(rest, depth+1))
		default:
			if _, _, ok := splitYAMLKey(rest); ok {
				// Inline mapping: rewrite this line as a plain mapping entry at the
				// content column and let parseMapping fold in the sibling keys that
				// follow at that column.
				p.lines[p.pos] = yamlLine{col: ln.contentCol, dash: false, contentCol: ln.contentCol, text: rest}
				out = append(out, p.parseMapping(ln.contentCol, depth+1))
			} else {
				p.pos++
				out = append(out, parseYAMLScalar(rest))
			}
		}
	}
	return out
}

// parseBlock dispatches a block value that begins at column indent to either a
// sequence (if it opens with a dash) or a mapping.
func (p *yamlParser) parseBlock(indent, depth int) any {
	if depth > maxParseDepth {
		return nil // partial value; see parseMapping's cap note
	}
	if !p.done() && p.cur().dash && p.cur().col == indent {
		return p.parseSequence(indent, depth+1)
	}
	return p.parseMapping(indent, depth+1)
}

// parseYAMLInline parses a value that sits on the same line as its key or dash:
// a flow mapping, a flow sequence, or a scalar.
func parseYAMLInline(s string, depth int) any {
	s = strings.TrimSpace(s)
	if depth > maxParseDepth {
		// Too deep to keep unwrapping flow collections: hand back the still-nested
		// text as a raw scalar rather than recursing into another stack frame.
		return parseYAMLScalar(s)
	}
	switch {
	case strings.HasPrefix(s, "{"):
		return parseFlowMapping(s, depth+1)
	case strings.HasPrefix(s, "["):
		return parseFlowSequence(s, depth+1)
	default:
		return parseYAMLScalar(s)
	}
}

// parseFlowMapping parses "{ k: v, k2: v2, nested: { … } }" into a map. Values
// may themselves be flow collections, so parsing recurses.
func parseFlowMapping(s string, depth int) map[string]any {
	m := map[string]any{}
	if depth > maxParseDepth {
		return m // partial value; see parseYAMLInline's cap note
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	for _, part := range splitTopLevel(s, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := splitYAMLKey(part)
		if !ok {
			continue
		}
		m[key] = parseYAMLInline(val, depth+1)
	}
	return m
}

// parseFlowSequence parses "[a, b, { … }]" into a slice, recursing into nested
// flow values.
func parseFlowSequence(s string, depth int) []any {
	var out []any
	if depth > maxParseDepth {
		return out // partial (nil) value; see parseYAMLInline's cap note
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	for _, part := range splitTopLevel(s, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, parseYAMLInline(part, depth+1))
	}
	return out
}

// parseYAMLScalar strips single/double quotes (unescaping the common cases) and
// returns the raw scalar text otherwise. Integers and booleans are left as their
// string form for callers to coerce.
func parseYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			return unquoteDoubleYAML(s[1 : len(s)-1])
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return s
}

// unquoteDoubleYAML unescapes the handful of backslash escapes a Clash config
// realistically uses inside a double-quoted scalar.
func unquoteDoubleYAML(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// splitYAMLKey splits "key: value" where the colon is followed by whitespace or
// ends the string, returning the trimmed (and unquoted) key and the trimmed
// value. A colon that is merely part of a value — a URL "https://…" — does not
// split, so a bare scalar reports ok=false and is treated as a sequence element
// rather than a mapping.
func splitYAMLKey(s string) (key, val string, ok bool) {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inDouble:
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '"':
			inDouble = true
		case c == '\'':
			inSingle = true
		case c == ':':
			if i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t' {
				return parseYAMLScalar(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// splitTopLevel splits s on sep, ignoring separators nested inside {}, [] or
// quotes. It powers flow-collection parsing.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth := 0
	inSingle, inDouble := false, false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inDouble:
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '"':
			inDouble = true
		case c == '\'':
			inSingle = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// clashProxyMaps decodes a Clash/Mihomo config and returns its "proxies" entries
// as maps, one per proxy, in order. present reports whether a top-level proxies
// key existed at all. A non-map entry (which a well-formed config never has) is
// returned as a nil map so the caller still counts it as skipped.
func clashProxyMaps(body []byte) (proxies []map[string]any, present bool) {
	raw, ok := decodeYAML(body)["proxies"]
	if !ok {
		return nil, false
	}
	seq, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	for _, item := range seq {
		if m, ok := item.(map[string]any); ok {
			proxies = append(proxies, m)
		} else {
			proxies = append(proxies, nil)
		}
	}
	return proxies, true
}
