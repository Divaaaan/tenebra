// Package state owns the sole canonical desktop repository and its revisions.
package state

import (
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/profile"
)

const SchemaVersion = 2
const PreferencesVersion = 3
const AnyRevision uint64 = 0

type Resolution string

const (
	Resolved       Resolution = "resolved"
	NeedsSelection Resolution = "needs_selection"
)

type SelectionKind string

const (
	Auto   SelectionKind = "auto"
	Pinned SelectionKind = "pinned"
)

type NodeRef struct {
	ProfileID  string     `json:"profileId,omitempty"`
	NodeID     string     `json:"nodeId,omitempty"`
	Resolution Resolution `json:"resolution,omitempty"`
	IssueID    string     `json:"issueId,omitempty"`
}
type Selection struct {
	Kind SelectionKind `json:"kind"`
	Ref  NodeRef       `json:"ref"`
}
type Binding struct {
	ProfileID string  `json:"profileId"`
	Entry     NodeRef `json:"entry"`
	Exit      NodeRef `json:"exit"`
}
type ConnectionIntent struct {
	Autoconnect bool      `json:"autoconnect"`
	ProfileID   string    `json:"profileId,omitempty"`
	Selection   Selection `json:"selection"`
}
type MultihopRequirement struct {
	Enabled  bool      `json:"enabled"`
	Bindings []Binding `json:"bindings"`
}
type ResolutionIssue struct {
	ID         string    `json:"id"`
	Reason     string    `json:"reason"`
	Field      string    `json:"field"`
	ProfileID  string    `json:"profileId,omitempty"`
	Candidates []NodeRef `json:"candidates,omitempty"`
}
type Snapshot struct {
	SchemaVersion    int                        `json:"schemaVersion"`
	IdentityVersion  int                        `json:"identityVersion"`
	MatcherVersion   int                        `json:"matcherVersion"`
	MigrationID      string                     `json:"migrationId"`
	Revision         uint64                     `json:"revision"`
	Profiles         map[string]profile.Profile `json:"profiles"`
	ProfileOrder     []string                   `json:"profileOrder"`
	Preferences      Preferences                `json:"preferences"`
	Connection       ConnectionIntent           `json:"connection"`
	Multihop         MultihopRequirement        `json:"multihop"`
	LastGood         map[string]NodeRef         `json:"lastGood"`
	Issues           []ResolutionIssue          `json:"issues"`
	AdditionalFields map[string]json.RawMessage `json:"-"`
}
type LegacyFile struct {
	Present bool
	Bytes   []byte
}
type LegacyBundle struct{ Profiles, Settings, LastGood LegacyFile }

var (
	ErrCorruptState       = errors.New("invalid canonical or legacy state")
	ErrUnsupportedSchema  = errors.New("unsupported canonical state version")
	ErrNeedsSelection     = errors.New("explicit selection required")
	ErrRevisionConflict   = errors.New("canonical revision conflict")
	ErrProfileConflict    = errors.New("profile revision conflict")
	ErrIntentConflict     = errors.New("connection intent changed")
	ErrRequiredReference  = errors.New("required reference cannot be preserved")
	ErrMaintenancePending = errors.New("maintenance_pending")
	ErrDurabilityUnknown  = errors.New("canonical durability outcome is unknown")
	ErrClosed             = errors.New("canonical repository is closed")
)
