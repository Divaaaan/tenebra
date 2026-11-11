package profile

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/model"
)

var errInvalidJSON = errors.New("invalid profile JSON object")

// Node's custom JSON methods must not swallow the containing Server.ID.
// Wrapper fields never enter the private connection-spec matcher.
func (s Server) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(s.Node)
	if err != nil {
		return nil, errInvalidJSON
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil, errInvalidJSON
	}
	fields["id"], _ = json.Marshal(s.ID)
	delete(fields, "insecure")
	if s.TLS != nil && s.TLS.Insecure {
		fields["insecure"] = json.RawMessage("true")
	}
	return json.Marshal(fields)
}

func (s *Server) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return errInvalidJSON
	}
	var id string
	if raw, ok := fields["id"]; ok && json.Unmarshal(raw, &id) != nil {
		return errInvalidJSON
	}
	delete(fields, "id")
	delete(fields, "insecure")
	value, err := json.Marshal(fields)
	if err != nil {
		return errInvalidJSON
	}
	var node model.Node
	if json.Unmarshal(value, &node) != nil {
		return errInvalidJSON
	}
	*s = Server{ID: id, Node: node}
	return nil
}

var profileKeys = []string{"id", "name", "source", "url", "nodes", "updatedAt", "expiresAt", "trafficUsed", "trafficTotal", "managed", "tier", "identityVersion", "revision", "httpAllowed"}

func (p Profile) MarshalJSON() ([]byte, error) {
	type plain Profile
	data, err := json.Marshal(plain(p))
	if err != nil {
		return nil, errInvalidJSON
	}
	if len(p.AdditionalFields) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil, errInvalidJSON
	}
	known := make(map[string]bool, len(profileKeys))
	for _, key := range profileKeys {
		known[key] = true
	}
	for key, value := range p.AdditionalFields {
		if !known[key] {
			if !json.Valid(value) {
				return nil, errInvalidJSON
			}
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func (p *Profile) UnmarshalJSON(data []byte) error {
	type plain Profile
	var value plain
	var fields map[string]json.RawMessage
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' || json.Unmarshal(data, &value) != nil || json.Unmarshal(data, &fields) != nil {
		return errInvalidJSON
	}
	for _, key := range profileKeys {
		delete(fields, key)
	}
	if len(fields) == 0 {
		fields = nil
	}
	*p = Profile(value)
	p.AdditionalFields = fields
	return nil
}
