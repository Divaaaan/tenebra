package subscription

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type Origin struct{ Scheme, Host, Port string }

var errInvalidOrigin = errors.New("invalid subscription origin")
var ErrNetworkRequired = errors.New("an authorized subscription network is required")
var ErrHTTPNotAllowed = errors.New("HTTP requires explicit profile approval")
var errHTTPSDowngrade = errors.New("HTTPS downgrade is forbidden")
var errRedirectForbidden = errors.New("redirect is forbidden for this operation")

func OriginOf(value *url.URL) (Origin, error) {
	if value == nil || value.Opaque != "" {
		return Origin{}, errInvalidOrigin
	}
	scheme, host := strings.ToLower(value.Scheme), strings.ToLower(value.Hostname())
	if (scheme != "https" && scheme != "http") || host == "" || strings.ContainsAny(host, "\x00\r\n\t %") {
		return Origin{}, errInvalidOrigin
	}
	port := value.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return Origin{}, errInvalidOrigin
	}
	return Origin{scheme, host, strconv.Itoa(parsed)}, nil
}
