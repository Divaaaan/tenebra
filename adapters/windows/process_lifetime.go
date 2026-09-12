package windows

import "errors"

// suspendedChild keeps the external process boundary injectable. Production
// Windows starts suspended, assigns an owner-only job, then resumes execution.
type suspendedChild interface {
	Prepare() error
	StartSuspended() error
	Assign() error
	Resume() error
	Kill() error
	Wait() error
	Close() error
}

func startWithLifetime(child suspendedChild) (func() error, error) {
	if err := child.Prepare(); err != nil {
		return nil, errors.Join(err, child.Close())
	}
	if err := child.StartSuspended(); err != nil {
		return nil, errors.Join(err, child.Close())
	}
	if err := child.Assign(); err != nil {
		return nil, errors.Join(err, child.Kill(), child.Close(), child.Wait())
	}
	if err := child.Resume(); err != nil {
		return nil, errors.Join(err, child.Kill(), child.Close(), child.Wait())
	}
	return child.Close, nil
}

type localStartError struct{ error }

func (localStartError) LocalSetupFailure() bool { return true }
func (e localStartError) Unwrap() error         { return e.error }
