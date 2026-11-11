// Package installtx owns the durable installer protocol. Journal contents are
// authority only after validation and a committed protected filesystem replace.
package installtx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Divaaaan/tenebra/core/state"
)

const (
	MaxMetadataBytes        = 64 << 10
	MaxContainerBytes int64 = 1 << 30
	MaxExpandedBytes  int64 = 2 << 30
	MaxArchiveEntries       = 8192
	MaxSelectedRoles        = 32
)

var (
	ErrProtocol               = errors.New("invalid installer protocol")
	ErrSignature              = errors.New("installer signature verification failed")
	ErrConflict               = errors.New("installer journal generation conflict")
	ErrRecoveryRequired       = errors.New("installer recovery required")
	ErrReadiness              = errors.New("installer readiness verification failed")
	ErrUnknownService         = errors.New("unknown service configuration preserved")
	ErrServiceManager         = errors.New("native service manager unavailable or unconfirmed")
	ErrLegacyAdoptionRequired = errors.New("known legacy service requires protected snapshot adoption")
	ErrUntrustedHelperRole    = errors.New("helper is outside its trusted installed role")
)

type Source struct {
	Layout        string `json:"layout"`
	AbsolutePath  string `json:"absolute_path"`
	SignaturePath string `json:"signature_path"`
}
type ManifestFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}
type Manifest struct {
	Schema    uint32         `json:"schema"`
	Kind      string         `json:"kind"`
	Version   string         `json:"version"`
	SourceSHA string         `json:"source_sha"`
	Target    string         `json:"target"`
	Files     []ManifestFile `json:"files"`
}
type RuntimeRef struct {
	ArtifactSHA256 string `json:"artifact_sha256"`
	Version        string `json:"version"`
	HelperSHA256   string `json:"helper_sha256"`
	Provenance     string `json:"provenance"`
}
type StagedPayload struct {
	StageID  string
	Runtime  RuntimeRef
	Manifest Manifest
}
type ProcessIdentity struct {
	PID                  uint32
	StartToken, LaunchID string
}
type CheckpointRef = state.CheckpointRef
type SchemaReceipt = state.SchemaReceipt
type Transaction struct {
	ID                  string         `json:"id"`
	Action              string         `json:"action"`
	Phase               string         `json:"phase"`
	OperationalCommitID string         `json:"operational_commit_id"`
	RestartAck          string         `json:"restart_ack"`
	Trust               string         `json:"trust"`
	ErrorCode           string         `json:"error_code"`
	PriorActive         *RuntimeRef    `json:"prior_active"`
	Candidate           *RuntimeRef    `json:"candidate"`
	Checkpoint          *CheckpointRef `json:"checkpoint"`
	SchemaReceipt       *SchemaReceipt `json:"schema_receipt"`
}
type Journal struct {
	Schema      uint32       `json:"schema"`
	Generation  uint64       `json:"generation"`
	Manager     string       `json:"manager"`
	Active      *RuntimeRef  `json:"active"`
	Pending     *RuntimeRef  `json:"pending"`
	Transaction *Transaction `json:"transaction"`
}
type Status struct {
	Phase               string `json:"phase"`
	Manager             string `json:"manager"`
	ActiveVersion       string `json:"active_version"`
	PendingVersion      string `json:"pending_version"`
	TransactionID       string `json:"transaction_id"`
	OperationalCommitID string `json:"operational_commit_id"`
	BusinessAdmission   string `json:"business_admission"`
	ErrorCode           string `json:"error_code"`
}
type Result struct {
	Status  Status
	Changed bool
}
type ApplyRequest struct {
	Schema                uint32  `json:"schema"`
	OperationID           string  `json:"operation_id"`
	Action                string  `json:"action"`
	RestartAck            string  `json:"restart_ack,omitempty"`
	Trust                 string  `json:"trust,omitempty"`
	ExpectedVersion       string  `json:"expected_version,omitempty"`
	ExpectedSourceSHA     string  `json:"expected_source_sha,omitempty"`
	ExpectedPayloadSHA256 string  `json:"expected_payload_sha256,omitempty"`
	Source                *Source `json:"source,omitempty"`
	KeepProfiles          bool    `json:"keep_profiles,omitempty"`
}

func hexValue(s string, size int) bool {
	if len(s) != size || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func protocolID(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && hexValue(strings.TrimPrefix(s, prefix), 32)
}
func newID(prefix string) (string, error) {
	if prefix != "it1_" && prefix != "oc1_" && prefix != "so1_" && prefix != "" {
		return "", ErrProtocol
	}
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data[:]), nil
}
func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
func safeToken(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._+-_", c)) {
			return false
		}
	}
	return true
}
func validRuntime(r *RuntimeRef) bool {
	return r == nil || hexValue(r.ArtifactSHA256, 64) && hexValue(r.HelperSHA256, 64) && safeToken(r.Version, 128) &&
		oneOf(r.Provenance, "release", "package_manager", "operator_dev", "legacy_root_owned")
}
func sameRuntime(a, b *RuntimeRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (j Journal) Validate() error {
	if j.Schema != 1 || j.Generation == 0 || !oneOf(j.Manager, "standalone", "dpkg", "pacman") || !validRuntime(j.Active) || !validRuntime(j.Pending) {
		return ErrProtocol
	}
	tx := j.Transaction
	if tx == nil {
		if j.Active != nil || j.Pending != nil {
			return ErrProtocol
		}
		return nil
	}
	if !protocolID(tx.ID, "it1_") || !oneOf(tx.Action, "install", "apply_pending", "remove") ||
		!oneOf(tx.Phase, "pending", "staged", "fencing", "fenced", "schema_ready", "ready", "committed_closed", "committed_open", "rolling_back", "rolled_back", "removed", "blocked") ||
		!oneOf(tx.Trust, "release", "package_manager", "operator_dev") || !validRuntime(tx.PriorActive) || !validRuntime(tx.Candidate) ||
		(tx.RestartAck != "" && !safeToken(tx.RestartAck, 128)) || (tx.ErrorCode != "" && !safeToken(tx.ErrorCode, 96)) {
		return ErrProtocol
	}
	if tx.Checkpoint != nil && (!safeToken(tx.Checkpoint.ID, 96) || !hexValue(tx.Checkpoint.SHA256, 64)) {
		return ErrProtocol
	}
	if tx.SchemaReceipt != nil && (tx.SchemaReceipt.Schema != state.SchemaVersion || tx.SchemaReceipt.Revision == 0 ||
		!safeToken(tx.SchemaReceipt.MigrationID, 96) || !hexValue(tx.SchemaReceipt.CanonicalSHA256, 64)) {
		return ErrProtocol
	}
	post := oneOf(tx.Phase, "committed_closed", "committed_open", "removed")
	if tx.OperationalCommitID != "" && !protocolID(tx.OperationalCommitID, "oc1_") {
		return ErrProtocol
	}
	if post && tx.OperationalCommitID == "" || !post && tx.Phase != "blocked" && tx.OperationalCommitID != "" {
		return ErrProtocol
	}
	if tx.Action == "remove" {
		if tx.Candidate != nil || j.Pending != nil || tx.SchemaReceipt != nil || oneOf(tx.Phase, "schema_ready", "ready", "committed_closed", "committed_open", "pending", "staged") {
			return ErrProtocol
		}
		if tx.Phase == "removed" && j.Active != nil {
			return ErrProtocol
		}
	} else {
		if tx.Candidate == nil || tx.Phase == "removed" {
			return ErrProtocol
		}
		if oneOf(tx.Phase, "schema_ready", "ready", "committed_closed", "committed_open") && (tx.Checkpoint == nil || tx.SchemaReceipt == nil) {
			return ErrProtocol
		}
		if oneOf(tx.Phase, "committed_closed", "committed_open") && (!sameRuntime(j.Active, tx.Candidate) || j.Pending != nil) {
			return ErrProtocol
		}
		if oneOf(tx.Phase, "pending", "staged") && (tx.Checkpoint != nil || tx.SchemaReceipt != nil || !sameRuntime(j.Pending, tx.Candidate)) {
			return ErrProtocol
		}
	}
	if !post && tx.Phase != "blocked" && !sameRuntime(j.Active, tx.PriorActive) {
		return ErrProtocol
	}
	if tx.Phase == "blocked" && tx.OperationalCommitID != "" && tx.Action != "remove" && !sameRuntime(j.Active, tx.Candidate) {
		return ErrProtocol
	}
	return nil
}

func DecodeApplyRequest(data []byte) (ApplyRequest, error) {
	var r ApplyRequest
	if err := strictDecode(data, &r); err != nil {
		return r, err
	}
	var keys map[string]jsonValue
	if err := decodeObject(data, &keys); err != nil {
		return r, err
	}
	required := []string{"schema", "operation_id", "action"}
	switch r.Action {
	case "install":
		required = append(required, "trust", "expected_version", "expected_source_sha", "expected_payload_sha256", "source")
	case "apply_pending":
		required = append(required, "expected_payload_sha256")
	case "remove":
		required = append(required, "keep_profiles")
	default:
		return r, ErrProtocol
	}
	allowed := append([]string{}, required...)
	if r.Action == "install" {
		allowed = append(allowed, "restart_ack")
	}
	for _, key := range required {
		if _, ok := keys[key]; !ok {
			return r, ErrProtocol
		}
	}
	for key := range keys {
		if !oneOf(key, allowed...) {
			return r, ErrProtocol
		}
	}
	return r, r.Validate()
}
func (r ApplyRequest) Validate() error {
	if r.Schema != 1 || !protocolID(r.OperationID, "so1_") {
		return ErrProtocol
	}
	switch r.Action {
	case "install":
		if !oneOf(r.Trust, "release", "package_manager", "operator_dev") || !safeToken(r.ExpectedVersion, 128) ||
			!hexValue(r.ExpectedSourceSHA, 40) || !hexValue(r.ExpectedPayloadSHA256, 64) || r.Source == nil || r.KeepProfiles ||
			(r.RestartAck != "" && !safeToken(r.RestartAck, 128)) {
			return ErrProtocol
		}
		s := r.Source
		if !oneOf(s.Layout, "flat", "tauri_app_archive", "deb_archive", "installed_package", "tauri_deb_tree") ||
			len(s.AbsolutePath) == 0 || len(s.AbsolutePath) > 4096 || strings.ContainsRune(s.AbsolutePath, 0) ||
			len(s.SignaturePath) > 4096 || strings.ContainsRune(s.SignaturePath, 0) || r.Trust == "release" && s.SignaturePath == "" {
			return ErrProtocol
		}
	case "apply_pending":
		if !hexValue(r.ExpectedPayloadSHA256, 64) || r.Source != nil || r.Trust != "" || r.ExpectedVersion != "" || r.ExpectedSourceSHA != "" || r.RestartAck != "" || r.KeepProfiles {
			return ErrProtocol
		}
	case "remove":
		if !r.KeepProfiles || r.Source != nil || r.Trust != "" || r.ExpectedVersion != "" || r.ExpectedSourceSHA != "" || r.ExpectedPayloadSHA256 != "" || r.RestartAck != "" {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}
