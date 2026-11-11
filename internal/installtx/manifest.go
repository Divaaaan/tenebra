package installtx

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

var payloadRoles = map[string]uint32{
	"tenebra-core": 0755, "sing-box": 0755,
	"geoip-ru.srs": 0644, "geosite-ru.srs": 0644, "geosite-ads.srs": 0644,
}

func CanonicalManifest(m Manifest) ([]byte, error) {
	if m.Schema != 1 || !validManifestBuild(m) ||
		!oneOf(m.Target, "linux-amd64", "darwin-universal") || len(m.Files) != len(payloadRoles) || len(m.Files) > MaxSelectedRoles {
		return nil, ErrProtocol
	}
	m.Files = append([]ManifestFile{}, m.Files...)
	seen := map[string]bool{}
	var expanded int64
	for _, f := range m.Files {
		mode, ok := payloadRoles[f.Name]
		if !ok || seen[f.Name] || f.Mode != mode || !hexValue(f.SHA256, 64) || f.Size <= 0 || f.Size > MaxExpandedBytes-expanded {
			return nil, ErrProtocol
		}
		seen[f.Name] = true
		expanded += f.Size
	}
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].Name < m.Files[b].Name })
	return encodeMetadata(m)
}

func validManifestBuild(m Manifest) bool {
	switch m.Kind {
	case "tenebra-install-payload":
		return safeToken(m.Version, 128) && hexValue(m.SourceSHA, 40)
	case "tenebra-legacy-snapshot":
		// A protected copy of an installed legacy runtime has no authenticated
		// build version or VCS revision. Its file hashes identify the snapshot.
		return m.Version == "legacy" && m.SourceSHA == "" && m.Target == "linux-amd64"
	default:
		return false
	}
}
func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := strictDecode(data, &m); err != nil {
		return m, err
	}
	canonical, err := CanonicalManifest(m)
	if err != nil {
		return m, err
	}
	// Only canonical payload.json bytes define an artifact, independent of the
	// enclosing archive's filename, compression or operator input path.
	if string(canonical) != string(data) {
		return m, ErrProtocol
	}
	return m, nil
}
func artifactHash(m Manifest) (string, error) {
	data, err := CanonicalManifest(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
