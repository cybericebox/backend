// Package brain is the HTTP client of cyber-brain, the external service behind the authoring assistant: the
// one-time enrollment over server-only TLS, the mutual-TLS calls (renewal and the assistant routes) and the mapping
// of its error envelope. The brain is a plain HTTPS/SSE service; this is not the laboratory agents' gRPC.
package brain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

const (
	// maxEnvelopeBytes caps what is read of an error body.
	maxEnvelopeBytes = 64 << 10
	// headerTimeout is how long the brain may take to start answering. A stream or a download has no overall
	// timeout: the caller's context ends it.
	headerTimeout = 30 * time.Second
	// maxJSONBytes caps the enrollment and renewal answers.
	maxJSONBytes = 1 << 20
)

// ErrUnreachable wraps a failure to talk to the brain (network, TLS handshake, timeout before the headers).
var ErrUnreachable = errors.New("cyber-brain is unreachable")

// ErrEnrollmentDenied is a refusal of the enrollment token or the request (a used, expired or unknown token).
var ErrEnrollmentDenied = errors.New("cyber-brain denied the enrollment")

// APIError is a brain error envelope with its HTTP status: {"error": "<code>", "message": "<human>"}. The platform
// maps on Status and Code, never on Message.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cyber-brain answered %d %s: %s", e.Status, e.Code, e.Message)
}

// Request is one call to the brain.
type Request struct {
	Method string
	// Path starts with /v1/.
	Path   string
	Query  url.Values
	Header http.Header
	Body   io.Reader
	// ContentLength is the body length, -1 when unknown (the body is then chunked).
	ContentLength int64
}

// Client calls the brain over mutual TLS with one client certificate. It is immutable: a renewed certificate is a new
// Client.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient builds the client of one stored connection. caPEM verifies the brain's server certificate; empty means the
// system roots.
func NewClient(endpoint string, certPEM, keyPEM, caPEM []byte) (*Client, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("client certificate and key: %w", err)
	}
	pool, err := rootPool(caPEM)
	if err != nil {
		return nil, err
	}
	return newClient(endpoint, &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12}), nil
}

func newClient(endpoint string, cfg *tls.Config) *Client {
	return &Client{endpoint: endpoint, http: &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       cfg,
			ResponseHeaderTimeout: headerTimeout,
			TLSHandshakeTimeout:   10 * time.Second,
			// An event stream must not be gzip-buffered, and the brain's answers are small or already compressed.
			DisableCompression:  true,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
		// A redirect would carry the subject header to another place: the brain does not redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func rootPool(caPEM []byte) (*x509.CertPool, error) {
	if len(caPEM) == 0 {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("server CA: no certificate found")
	}
	return pool, nil
}

// Close drops the idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Do sends the request. A 2xx answer is returned with its body open (the caller closes it); any other status becomes an
// *APIError and the body is consumed. A transport failure wraps ErrUnreachable.
func (c *Client) Do(ctx context.Context, in Request) (*http.Response, error) {
	u := url.URL{Scheme: "https", Host: c.endpoint, Path: in.Path, RawQuery: in.Query.Encode()}
	req, err := http.NewRequestWithContext(ctx, in.Method, u.String(), in.Body)
	if err != nil {
		return nil, err
	}
	for k, vs := range in.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if in.Body != nil {
		req.ContentLength = in.ContentLength
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, decodeEnvelope(resp)
}

func decodeEnvelope(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxEnvelopeBytes))
	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		e.Code, e.Message = envelope.Error, envelope.Message
	}
	return e
}

// Renew asks for a new certificate for the request over the current mutual-TLS connection.
func (c *Client) Renew(ctx context.Context, csrPEM string) (assistantModel.Enrollment, error) {
	return c.postJSON(ctx, "/v1/renew", map[string]string{"csr_pem": csrPEM})
}

func (c *Client) postJSON(ctx context.Context, path string, body any) (assistantModel.Enrollment, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return assistantModel.Enrollment{}, err
	}
	resp, err := c.Do(ctx, Request{
		Method: http.MethodPost, Path: path, Header: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}},
		Body: strings.NewReader(string(raw)), ContentLength: int64(len(raw)),
	})
	if err != nil {
		return assistantModel.Enrollment{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		CertPEM string `json:"cert_pem"`
		CAPEM   string `json:"ca_pem"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(&out); err != nil {
		return assistantModel.Enrollment{}, fmt.Errorf("cyber-brain returned an unreadable certificate answer: %w", err)
	}
	if out.CertPEM == "" {
		return assistantModel.Enrollment{}, errors.New("cyber-brain returned no certificate")
	}
	return assistantModel.Enrollment{CertPEM: out.CertPEM, CAPEM: out.CAPEM}, nil
}

// Enroll exchanges a one-time token and a certificate request for a client certificate. It runs over server-only TLS:
// the brain is verified with caPEM (empty: the system roots) and the client presents nothing yet.
func Enroll(ctx context.Context, endpoint string, caPEM []byte, token, csrPEM string) (assistantModel.Enrollment, error) {
	pool, err := rootPool(caPEM)
	if err != nil {
		return assistantModel.Enrollment{}, err
	}
	c := newClient(endpoint, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	defer c.Close()
	out, err := c.postJSON(ctx, "/v1/enroll", map[string]string{"token": token, "csr_pem": csrPEM})
	var api *APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
			return assistantModel.Enrollment{}, fmt.Errorf("%w (%d %s)", ErrEnrollmentDenied, api.Status, api.Code)
		}
	}
	return out, err
}
