//go:build !windows

package zapret

// EmbeddedVersion is empty in a build that carries no bundle.
//
// The control layer reports the installed version to the UI, and naming a
// release whose bytes are not in this binary would be a claim about something
// that does not exist here.
const EmbeddedVersion = ""

// InstallEmbedded is the non-Windows stub: there are no compiled-in bytes to
// unpack, and no winws.exe to run if there were. See ErrNoEmbeddedBundle.
func InstallEmbedded(string) ([]Strategy, error) { return nil, ErrNoEmbeddedBundle }
