//go:build linux || darwin

package installtx

import (
	"os"
	"strconv"
)

func CurrentOperatorPrincipal() (string, error) { return "uid:" + strconv.Itoa(os.Geteuid()), nil }
