package subscription

import (
	"net/url"
	"testing"
)

func TestOriginNormalizationIsExact(t *testing.T) {
	parse := func(raw string) Origin {
		t.Helper()
		value, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		origin, err := OriginOf(value)
		if err != nil {
			t.Fatal(err)
		}
		return origin
	}
	base := parse("https://HOST.test/private?key=x")
	if base != parse("HTTPS://host.test:443/other") {
		t.Fatal("case/default443 normalization failed")
	}
	for _, raw := range []string{"https://host.test:444/", "http://host.test:443/", "https://host.test./", "https://child.host.test/", "https://other.test/"} {
		if parse(raw) == base {
			t.Fatalf("origin authority silently expanded to %s", raw)
		}
	}
	for _, raw := range []string{"https:opaque", "ftp://host.test", "https://host.test:0/", "https://host.test:65536/"} {
		value, err := url.Parse(raw)
		if err == nil {
			if _, err := OriginOf(value); err == nil {
				t.Fatal("invalid origin accepted")
			}
		}
	}
}
