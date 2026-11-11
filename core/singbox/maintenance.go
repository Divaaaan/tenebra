package singbox

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/Divaaaan/tenebra/core/maintenance"
	"strings"
	"time"
)

var errMaintenanceConfig = errors.New("singbox: invalid private maintenance configuration")

func appendMaintenance(config map[string]any, private maintenance.InboundConfig) error {
	if private.RunID == "" || !private.Address.IsValid() || private.Address.Addr().String() != "127.0.0.1" || private.Address.Port() == 0 || private.Username == "" || private.Password == "" || len(private.Username) > 256 || len(private.Password) > 256 || strings.ContainsAny(private.Username, ":\r\n") || strings.ContainsAny(private.Password, "\r\n") || len(private.CertificatePEM) > 65536 || len(private.PrivateKeyPEM) > 16384 {
		return errMaintenanceConfig
	}
	pair, err := tls.X509KeyPair([]byte(private.CertificatePEM), []byte(private.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) != 1 {
		return errMaintenanceConfig
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errMaintenanceConfig
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	if _, err = certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errMaintenanceConfig
	}
	inbound := map[string]any{"type": "http", "tag": maintenanceTag, "listen": "127.0.0.1", "listen_port": int(private.Address.Port()), "users": []map[string]any{{"username": private.Username, "password": private.Password}}, "tls": map[string]any{"enabled": true, "min_version": "1.2", "certificate": []string{private.CertificatePEM}, "key": []string{private.PrivateKeyPEM}}}
	config["inbounds"] = append(config["inbounds"].([]map[string]any), inbound)
	route := config["route"].(map[string]any)
	rules := []map[string]any{{"inbound": []string{maintenanceTag}, "network": "tcp", "action": "route", "outbound": proxyTag}, {"inbound": []string{maintenanceTag}, "action": "reject"}}
	route["rules"] = append(rules, route["rules"].([]map[string]any)...)
	return nil
}
