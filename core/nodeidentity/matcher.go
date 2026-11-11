package nodeidentity

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/model"
)

var errInvalidSpec = errors.New("invalid node specification")

// Matcher keeps canonical connection specifications private. No digest, key or
// credential verifier is returned to callers or used as a public ID.
type Matcher struct{ candidates map[string][]int }

func canonicalRecord(node model.Node, includeName bool) (string, error) {
	if !includeName {
		node.Name = ""
	}
	data, err := json.Marshal(node)
	if err != nil {
		return "", errInvalidSpec
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", errInvalidSpec
	}
	data, err = json.Marshal(value)
	if err != nil {
		return "", errInvalidSpec
	}
	return string(data), nil
}

func NewMatcher(nodes []model.Node) (*Matcher, error) {
	matcher := &Matcher{candidates: make(map[string][]int, len(nodes))}
	for index, node := range nodes {
		key, err := canonicalRecord(node, false)
		if err != nil {
			return nil, err
		}
		matcher.candidates[key] = append(matcher.candidates[key], index)
	}
	return matcher, nil
}

func (m *Matcher) Candidates(node model.Node) ([]int, error) {
	if m == nil {
		return nil, errInvalidSpec
	}
	key, err := canonicalRecord(node, false)
	if err != nil {
		return nil, err
	}
	return append([]int(nil), m.candidates[key]...), nil
}
