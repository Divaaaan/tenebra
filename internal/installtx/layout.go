package installtx

import "github.com/Divaaaan/tenebra/internal/securefs"

type PlatformPaths struct{ Data, Maintenance, Releases, Helper, BusinessSocket, Status string }

func SystemPaths(goos string) (PlatformPaths, error) {
	switch goos {
	case "linux":
		return PlatformPaths{
			Data: "/var/lib/tenebra/data", Maintenance: "/var/lib/tenebra/maintenance", Releases: "/var/lib/tenebra/runtime/releases",
			Helper: "/usr/local/lib/tenebra/tenebra-helper", BusinessSocket: "/run/tenebra.sock", Status: "/run/tenebra-service-status.json",
		}, nil
	case "darwin":
		return PlatformPaths{
			Data: "/Library/Application Support/Tenebra/data", Maintenance: "/Library/Application Support/Tenebra/maintenance",
			Releases: "/Library/PrivilegedHelperTools/Tenebra/releases", Helper: "/Library/PrivilegedHelperTools/Tenebra/tenebra-helper",
			BusinessSocket: "/var/run/tenebra.sock", Status: "/var/run/tenebra-service-status.json",
		}, nil
	default:
		return PlatformPaths{}, securefs.ErrUnsupported
	}
}
