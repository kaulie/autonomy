package cursorsdk

import (
	"net/http"

	"connectrpc.com/connect"
)

// bearerTransport injects Authorization on every request (unary and stream).
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func newBridgeHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: bearerTransport{
			token: token,
			base: &http.Transport{
				Proxy:             nil, // never proxy loopback bridge traffic
				ForceAttemptHTTP2: false,
			},
		},
	}
}

func connectOpts() []connect.ClientOption {
	return nil
}
