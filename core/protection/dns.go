package protection

import (
	"fmt"
	"net/netip"
	"net/url"
)

// ValidateDNS refuses an implicit plaintext/system bootstrap. The caller keeps
// the user's saved value; changing this requirement must be an explicit choice.
func ValidateDNS(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "tls") {
		return fmt.Errorf("host protection requires an encrypted https:// or tls:// bootstrap resolver with a literal IP")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("host protection requires a literal-IP encrypted bootstrap resolver; hostname/system DNS is unavailable while blocked")
	}
	if u.Scheme == "tls" && (u.Path != "" && u.Path != "/" || u.RawQuery != "") {
		return fmt.Errorf("TLS DNS bootstrap does not accept a path or query")
	}
	return nil
}
