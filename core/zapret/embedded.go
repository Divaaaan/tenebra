package zapret

import "errors"

// ErrNoEmbeddedBundle is what InstallEmbedded returns in a build that carries no
// bundle at all.
//
// Only the Windows builds carry one. The bundle is winws.exe on the WinDivert
// driver — a Windows packet filter — and there is nothing on macOS or Linux that
// could run it (Runner there is a stub; see run_other.go). Compiling a megabyte
// and a half of it into those binaries would charge every download for bytes
// that can never execute, and the first connect that could not reach GitHub
// would unpack four megabytes of unusable Windows executables into the user's
// config directory.
//
// It is a distinct error rather than a plain one because the caller has to tell
// "this build has no bundle to install" apart from "the bundle would not
// install". The first is the platform and not a failure; reporting it as one
// would put a warning about a missing bypass on every macOS and Linux connect,
// where the tunnel carries everything anyway.
var ErrNoEmbeddedBundle = errors.New("zapret: вшитая сборка есть только в сборках для Windows")
