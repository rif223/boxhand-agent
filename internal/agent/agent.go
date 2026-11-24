// Package agent holds the identity of a Boxhand-Agent: its own TLS certificate, which the Boxhand on the other
// machine pins by fingerprint, and the hash of its API token.
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	certFile = "agent.crt"
	keyFile  = "agent.key"
	confFile = "agent.json"
)

type config struct {
	TokenKey string `json:"tokenKey"` // SHA-256 of the token in hex (api.AgentKey)
}

// Fingerprint formats the SHA-256 of a certificate as AB:CD:…, the way browsers show it.
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	s := strings.ToUpper(hex.EncodeToString(h[:]))
	parts := make([]string, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, ":")
}

// EnsureCert creates a self-signed certificate on first use and returns it with its fingerprint.
func EnsureCert(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := newCert(certPath, keyPath); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return c, Fingerprint(c.Certificate[0]), nil
}

func newCert(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "boxhand-agent " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0), // pinned by fingerprint, so no renewal is needed
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"boxhand-agent"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// NewToken creates a new token, stores only its key (computed by keyOf) and returns the token once.
func NewToken(dir string, keyOf func(string) string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "bha_" + base64.RawURLEncoding.EncodeToString(b)
	data, _ := json.MarshalIndent(config{TokenKey: keyOf(token)}, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, confFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return token, os.Rename(tmp, filepath.Join(dir, confFile))
}

// TokenKey returns the stored token key, or an error if the agent was not initialized yet.
func TokenKey(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, confFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("no token yet: run `boxhand-agent init` first")
	}
	if err != nil {
		return "", err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil || len(c.TokenKey) != 64 {
		return "", fmt.Errorf("%s is damaged: run `boxhand-agent init` again", confFile)
	}
	return c.TokenKey, nil
}
