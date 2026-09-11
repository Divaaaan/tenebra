package protection

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEncryptedBootstrapRefusesPlaintextAndHostnameWithoutNetwork(t *testing.T) {
	for _, value := range []string{"8.8.8.8", "udp://8.8.8.8", "https://dns.example.test/dns-query", "tls://dns.example.test", "http://1.1.1.1/dns-query", "quic://1.1.1.1"} {
		if err := ValidateDNS(value); err == nil {
			t.Fatal("invalid resolver accepted", value)
		}
		r := NewResolver(func() (string, bool) { return value, true })
		if _, err := r.Dial(context.Background(), "udp", "192.0.2.53:53"); err == nil {
			t.Fatal("invalid resolver attempted fallback", value)
		}
	}
	for _, value := range []string{"https://77.88.8.8/dns-query", "tls://1.1.1.1", "tls://[2606:4700:4700::1111]:853"} {
		if err := ValidateDNS(value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDNSConnFramesBoundedHTTPSReplyWithoutSocket(t *testing.T) {
	query := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	answer := append([]byte(nil), query...)
	answer[2] = 0x81
	called := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.String() != "https://192.0.2.53/dns-query" || !bytes.Equal(body, query) {
			t.Fatal("wrong encrypted query")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/dns-message"}}, Body: io.NopCloser(bytes.NewReader(answer))}, nil
	})}
	c := newDNSConn(context.Background(), "https://192.0.2.53/dns-query", client)
	defer c.Close()
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
	frame = append(frame, query...)
	if _, err := c.Write(frame[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(frame[1:]); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || called != 1 || !bytes.Equal(got[2:], answer) || int(binary.BigEndian.Uint16(got)) != len(answer) {
		t.Fatalf("got=%x called=%d err=%v", got, called, err)
	}
}
