// Package authority checks current business grants with a configured authority.
package authority

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/strictjson"
)

type Client struct {
	endpoint, token string
	client          *http.Client
}

func New(endpoint, token string) (*Client, error) {
	return NewWithCA(endpoint, token, "")
}

// NewWithCA creates an authority client and optionally extends the system
// trust store with a PEM encoded CA bundle. The CA file is only for the
// outbound authority connection; credentials remain in the Authorization
// header and are never read from the URL.
func NewWithCA(endpoint, token, caFile string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid authority configuration")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("authority requires HTTPS or loopback HTTP")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("authority CA file unavailable")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil || !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid authority CA bundle")
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &Client{endpoint, token, &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Authorize(ctx context.Context, p artifact.Principal, permission artifact.Permission) error {
	if !p.Valid() {
		return artifact.ErrForbidden
	}
	data, err := json.Marshal(struct {
		Principal  artifact.Principal  `json:"principal"`
		Permission artifact.Permission `json:"permission"`
	}{p, permission})
	if err != nil {
		return artifact.ErrDependency
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return artifact.ErrDependency
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", p.RequestID)
	response, err := c.client.Do(r)
	if err != nil {
		return artifact.ErrDependency
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return artifact.ErrForbidden
	}
	if response.StatusCode != http.StatusOK {
		return artifact.ErrDependency
	}
	var decision struct {
		Allowed bool `json:"allowed"`
	}
	if strictjson.Decode(response.Body, 1024, &decision) != nil {
		return artifact.ErrDependency
	}
	if !decision.Allowed {
		return artifact.ErrForbidden
	}
	return nil
}
