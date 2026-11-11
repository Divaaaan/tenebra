package subscription

import "strings"

// Only the top-level proxies section is tokenized/materialized. Unrelated YAML
// graphs are scanned as text, regardless of their breadth or nesting depth.
func clashProxyMapsWithLimits(body []byte, budget *parseBudget) ([]map[string]any, error) {
	var proxies []map[string]any
	text := stripBOM(string(body))
	active, found := false, false
	entryIndent := -1
	var record strings.Builder
	appendValue := func(value any) {
		mapping, _ := value.(map[string]any)
		proxies = append(proxies, mapping)
	}
	flush := func() error {
		if record.Len() == 0 {
			return nil
		}
		parser := &yamlParser{lines: tokenizeYAML([]byte(record.String()))}
		if !parser.done() {
			values := parser.parseSequence(entryIndent, 0)
			if len(values) > 0 {
				appendValue(values[0])
			} else {
				appendValue(nil)
			}
		}
		record.Reset()
		return nil
	}
	for len(text) > 0 {
		line, remaining, more := strings.Cut(text, "\n")
		text = remaining
		line = strings.TrimRight(stripYAMLComment(line), " \t\r")
		column := 0
		for column < len(line) && (line[column] == ' ' || line[column] == '\t') {
			column++
		}
		if column == len(line) {
			if !more {
				break
			}
			continue
		}
		content := line[column:]
		dash := content[0] == '-' && (len(content) == 1 || content[1] == ' ' || content[1] == '\t')
		if column == 0 && !dash {
			if active {
				if err := flush(); err != nil {
					return nil, err
				}
				active = false
			}
			key, value, ok := splitYAMLKey(content)
			if !ok || key != "proxies" {
				if !more {
					break
				}
				continue
			}
			if found {
				return nil, errInvalidLimits
			} // ambiguous repeated root key
			found = true
			if strings.HasPrefix(strings.TrimSpace(value), "[") {
				sequence := strings.TrimSpace(value)
				sequence = strings.TrimPrefix(sequence, "[")
				sequence = strings.TrimSuffix(sequence, "]")
				err := visitTopLevel(sequence, ',', func(item string) error {
					item = strings.TrimSpace(item)
					if item == "" {
						return nil
					}
					if err := budget.takeEntry(); err != nil {
						return err
					}
					if err := budget.rawRecord(len(item)); err != nil {
						return err
					}
					appendValue(parseYAMLInline(item, 0))
					return nil
				})
				if err != nil {
					return nil, err
				}
			} else if strings.TrimSpace(value) == "" {
				active = true
			}
			if !more {
				break
			}
			continue
		}
		if active {
			if dash && (entryIndent < 0 || column == entryIndent) {
				if err := flush(); err != nil {
					return nil, err
				}
				if err := budget.takeEntry(); err != nil {
					return nil, err
				}
				entryIndent = column
			}
			if entryIndent >= 0 {
				if err := budget.rawRecord(record.Len() + len(line) + 1); err != nil {
					return nil, err
				}
				record.WriteString(line)
				record.WriteByte('\n')
			}
		}
		if !more {
			break
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return proxies, nil
}

// The callback sees each flow entry before a sequence or its elements can be
// allocated. The old splitTopLevel helper materializes every part first.
func visitTopLevel(text string, separator byte, visit func(string) error) error {
	depth, start := 0, 0
	single, double := false, false
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch {
		case double:
			if char == '\\' {
				index++
				continue
			}
			if char == '"' {
				double = false
			}
		case single:
			if char == '\'' {
				single = false
			}
		case char == '"':
			double = true
		case char == '\'':
			single = true
		case char == '{' || char == '[':
			depth++
		case char == '}' || char == ']':
			if depth > 0 {
				depth--
			}
		case char == separator && depth == 0:
			if err := visit(text[start:index]); err != nil {
				return err
			}
			start = index + 1
		}
	}
	return visit(text[start:])
}
