package installtx

import "encoding/json"

// Public status is an informational projection of the durable P0 phase. It
// NEVER authenticates a process, authorizes admission or proves live readiness.
// Only the fixed Status DTO is serializable here; paths/hashes/profiles/URLs
// cannot be added by a request or by forwarding an arbitrary diagnostic map.
func EncodePublicStatus(s Status) ([]byte, error) {
	if err := validatePublicStatus(s); err != nil {
		return nil, err
	}
	body, err := json.Marshal(s)
	if err != nil || len(body)+1 > MaxMetadataBytes {
		return nil, ErrProtocol
	}
	return append(body, '\n'), nil
}
func DecodePublicStatus(body []byte) (Status, error) {
	var s Status
	if err := strictDecode(body, &s); err != nil {
		return Status{}, err
	}
	var fields map[string]jsonValue
	if decodeObject(body, &fields) != nil || len(fields) != 8 {
		return Status{}, ErrProtocol
	}
	for _, name := range []string{"phase", "manager", "active_version", "pending_version", "transaction_id", "operational_commit_id", "business_admission", "error_code"} {
		raw, ok := fields[name]
		if !ok {
			return Status{}, ErrProtocol
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return Status{}, ErrProtocol
		}
		if _, ok := value.(string); !ok {
			return Status{}, ErrProtocol
		}
	}
	if err := validatePublicStatus(s); err != nil {
		return Status{}, err
	}
	return s, nil
}
func validatePublicStatus(s Status) error {
	if !oneOf(s.Phase, "inactive", "pending", "staged", "fencing", "fenced", "schema_ready", "ready", "committed_closed", "committed_open", "rolling_back", "rolled_back", "removed", "blocked") ||
		!oneOf(s.Manager, "standalone", "dpkg", "pacman") || !oneOf(s.BusinessAdmission, "closed", "open", "unchanged") ||
		s.ActiveVersion != "" && !safeToken(s.ActiveVersion, 128) || s.PendingVersion != "" && !safeToken(s.PendingVersion, 128) ||
		s.ErrorCode != "" && !safeToken(s.ErrorCode, 64) || s.TransactionID != "" && !protocolID(s.TransactionID, "it1_") ||
		s.OperationalCommitID != "" && !protocolID(s.OperationalCommitID, "oc1_") {
		return ErrProtocol
	}
	if s.Phase == "inactive" {
		if s.ActiveVersion != "" || s.PendingVersion != "" || s.TransactionID != "" || s.OperationalCommitID != "" || s.ErrorCode != "" || s.BusinessAdmission != "closed" {
			return ErrProtocol
		}
	} else if s.TransactionID == "" {
		return ErrProtocol
	}
	if s.BusinessAdmission == "open" && s.Phase != "committed_open" || s.BusinessAdmission == "unchanged" && (!oneOf(s.Phase, "pending", "staged") || s.ActiveVersion == "") {
		return ErrProtocol
	}
	if s.Phase == "committed_open" && s.OperationalCommitID == "" {
		return ErrProtocol
	}
	return nil
}
