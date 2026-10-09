package theater

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OriginPolicy decides which pages may open a WebSocket.
//
// The handshake is authenticated by cookie, and a browser sends cookies on a
// WebSocket handshake whichever site's page opened it. The Origin header is
// the only thing that says which page that was, so it is checked before
// anything else (docs/decisions.md, D035).
type OriginPolicy struct {
	// public is the web app's origin, "scheme://host[:port]", in lower case.
	public string
}

// NewOriginPolicy returns the policy for a web app served at publicBaseURL.
func NewOriginPolicy(publicBaseURL string) (OriginPolicy, error) {
	u, err := url.Parse(publicBaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return OriginPolicy{}, fmt.Errorf("PUBLIC_BASE_URL %q is not an http or https URL", publicBaseURL)
	}
	return OriginPolicy{public: strings.ToLower(u.Scheme + "://" + u.Host)}, nil
}

// Allowed reports whether the request's Origin may open a WebSocket. Two
// origins may:
//
//   - the web app's own, which is what a browser sends whether it reaches
//     this server through the Next.js proxy, through the load balancer, or
//     directly on its port;
//   - one whose host and port are the ones this request was addressed to,
//     which is a page this server's own address served.
//
// A request with no Origin is refused. Browsers always send one on a
// WebSocket handshake, so only other programs are turned away, and they can
// set the header.
func (p OriginPolicy) Allowed(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.ToLower(u.Scheme+"://"+u.Host) == p.public {
		return true
	}
	return strings.EqualFold(u.Host, r.Host)
}
