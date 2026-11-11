package control

import (
	"context"

	"github.com/Divaaaan/tenebra/core/state"
)

// A narrow latest-state mutation: an automatic strategy choice never writes
// the user's on/off answer, or a captured copy of all preferences.
func (d *Daemon) commitZapretPreference(ctx context.Context, wanted *bool, strategy *string) error {
	if d.canonical == nil {
		return nil
	}
	_, err := d.CommitCanonical(ctx, state.AnyRevision, func(s *state.Snapshot) error {
		if wanted != nil {
			value := *wanted
			s.Preferences.ZapretEnabled = &value
		}
		if strategy != nil {
			s.Preferences.ZapretStrategy = *strategy
		}
		return nil
	})
	return err
}
