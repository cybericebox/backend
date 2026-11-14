// Package brainfake is a stand-in cyber-brain for tests: an HTTPS server with its own CA that enrolls and renews client
// certificates (server-only TLS for /v1/enroll, mutual TLS for everything else) and hands the assistant routes to a
// test handler. It lives outside _test files so the client, use case and HTTP handler tests share one fake.
package brainfake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Tenant is the CN of every certificate the fake issues.
const Tenant = "platform-test"

// Token is the one enrollment token the fake accepts (once).
const Token = "enroll-token"

// Fake is the running stand-in.
type Fake struct {
	Server *httptest.Server
	// CAPEM verifies the server certificate.
	CAPEM []byte
	// CertTTL is the lifetime of the certificates it issues (default 90 days).
	CertTTL time.Duration
	// Assistant answers /v1/assistant/* (after the client certificate was verified).
	Assistant http.Handler

	mu          sync.Mutex
	caCert      *x509.Certificate
	caKey       *ecdsa.PrivateKey
	tokenUsed   bool
	enrolls     int
	renews      int
	lastCSRs    []string
	renewStatus int // when not 0, /v1/renew answers it with an error envelope
	issuedSerie int64
}

// New starts the fake. Close it with Fake.Server.Close.
func New() *Fake {
	f := &Fake{CertTTL: 90 * 24 * time.Hour}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "brainfake-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	f.caCert, _ = x509.ParseCertificate(der)
	f.caKey = caKey
	f.CAPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "brain.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, _ := x509.CreateCertificate(rand.Reader, serverTmpl, f.caCert, &serverKey.PublicKey, caKey)

	pool := x509.NewCertPool()
	pool.AddCert(f.caCert)
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.Server.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}},
		ClientCAs:    pool, ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS12,
	}
	f.Server.StartTLS()
	return f
}

// Endpoint is host:port of the running fake.
func (f *Fake) Endpoint() string { return f.Server.Listener.Addr().String() }

// Enrolls and Renews count the accepted calls.
func (f *Fake) Enrolls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.enrolls }
func (f *Fake) Renews() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.renews }

// FailRenewWith makes /v1/renew answer this status with an error envelope (0: normal).
func (f *Fake) FailRenewWith(status int) { f.mu.Lock(); defer f.mu.Unlock(); f.renewStatus = status }

// Fail writes the brain's error envelope.
func Fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/enroll":
		f.enroll(w, r)
	case "/v1/renew":
		f.renew(w, r)
	default:
		if len(r.TLS.VerifiedChains) == 0 {
			Fail(w, http.StatusUnauthorized, "client_certificate_required", "mutual TLS is required")
			return
		}
		if f.Assistant == nil {
			Fail(w, http.StatusNotFound, "not_found", "no such route")
			return
		}
		f.Assistant.ServeHTTP(w, r)
	}
}

func (f *Fake) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token  string `json:"token"`
		CSRPEM string `json:"csr_pem"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "malformed")
		return
	}
	f.mu.Lock()
	if in.Token != Token || f.tokenUsed {
		f.mu.Unlock()
		Fail(w, http.StatusForbidden, "enrollment_denied", "the token is not valid")
		return
	}
	f.tokenUsed = true
	f.enrolls++
	f.lastCSRs = append(f.lastCSRs, in.CSRPEM)
	f.mu.Unlock()
	f.issue(w, in.CSRPEM)
}

func (f *Fake) renew(w http.ResponseWriter, r *http.Request) {
	if len(r.TLS.VerifiedChains) == 0 {
		Fail(w, http.StatusUnauthorized, "client_certificate_required", "mutual TLS is required")
		return
	}
	var in struct {
		CSRPEM string `json:"csr_pem"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "malformed")
		return
	}
	f.mu.Lock()
	if f.renewStatus != 0 {
		status := f.renewStatus
		f.mu.Unlock()
		Fail(w, status, "renew_failed", "renewal refused")
		return
	}
	f.renews++
	f.lastCSRs = append(f.lastCSRs, in.CSRPEM)
	f.mu.Unlock()
	f.issue(w, in.CSRPEM)
}

func (f *Fake) issue(w http.ResponseWriter, csrPEM string) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		Fail(w, http.StatusBadRequest, "bad_csr", "no PEM")
		return
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		Fail(w, http.StatusBadRequest, "bad_csr", "unreadable request")
		return
	}
	f.mu.Lock()
	f.issuedSerie++
	serial := big.NewInt(100 + f.issuedSerie)
	ttl := f.CertTTL
	f.mu.Unlock()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: Tenant},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(ttl),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "internal", "cannot sign")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"cert_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"ca_pem":   string(f.CAPEM),
	})
}
