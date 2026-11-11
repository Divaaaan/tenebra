package control

import (
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
)

type ProfileStore interface {
	Dir() string
	List() []profile.Profile
	Get(string) (profile.Profile, bool)
	Add(profile.Profile) error
	Update(profile.Profile) error
	Remove(string) error
}

var _ ProfileStore = (*profile.Store)(nil)
var _ ProfileStore = (*state.ProfileView)(nil)
