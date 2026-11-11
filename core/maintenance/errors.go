package maintenance

import "errors"

var (
	ErrInvalidLease            = errors.New("maintenance_invalid_lease")
	ErrRevoked                 = errors.New("maintenance_revoked")
	ErrResolverUnavailable     = errors.New("maintenance_resolver_unavailable")
	ErrResolverAlreadyBound    = errors.New("maintenance_resolver_already_bound")
	ErrNumericAddressRequired  = errors.New("maintenance_numeric_address_required")
	ErrRawRegistrationRequired = errors.New("maintenance_raw_registration_required")
	ErrNetworkUnavailable      = errors.New("maintenance_network_unavailable")
	ErrResourceLimit           = errors.New("maintenance_resource_limit")
	ErrCommitPending           = errors.New("maintenance_commit_pending")
	ErrDrainPending            = errors.New("maintenance_drain_pending")
)

// causeError keeps error identity without formatting private dial/resolver
// errors, addresses, or cancellation reasons into public error strings.
type causeError struct{ code, cause error }

func (e *causeError) Error() string   { return e.code.Error() }
func (e *causeError) Unwrap() []error { return []error{e.code, e.cause} }
