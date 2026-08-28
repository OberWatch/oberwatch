package upgrade

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// CheckTimeout bounds one release metadata read.
	CheckTimeout = 8 * time.Second

	// DownloadTimeout bounds one artifact download, including verification of
	// what was downloaded.
	DownloadTimeout = 5 * time.Minute

	// maxRedirects bounds an artifact download's redirect chain. Release asset
	// URLs redirect once to the release CDN; a longer chain is a sign the
	// request is being steered somewhere else.
	maxRedirects = 5

	// userAgent identifies these requests. It carries no version, host name or
	// any other detail about the installation.
	userAgent = "oberwatch-upgrade"
)

var (
	// errRedirectRefused stops the release metadata read from being redirected
	// anywhere. The metadata endpoint does not redirect.
	errRedirectRefused = errors.New("release metadata requests do not follow redirects")

	// errRedirectNotAllowed stops an artifact download from being steered off
	// the release host.
	errRedirectNotAllowed = errors.New("release artifact redirect is not allowed")
)

// artifactRedirectHosts is the closed set of hosts an artifact download may be
// redirected to. Release asset URLs on github.com answer with a redirect to the
// release asset CDN, so redirects cannot simply be refused; they can be pinned.
var artifactRedirectHosts = []string{
	"github.com",
	"api.github.com",
	".githubusercontent.com",
}

// newCheckClient builds the client used to read release metadata: bounded,
// credential-free, and refusing every redirect.
func newCheckClient() *http.Client {
	return &http.Client{
		Timeout:       CheckTimeout,
		CheckRedirect: refuseRedirect,
		Transport:     newTransport(CheckTimeout),
	}
}

// newArtifactClient builds the client used to download release artifacts:
// bounded, credential-free, and allowed to follow only a short redirect chain
// that stays on the release hosts.
func newArtifactClient() *http.Client {
	return &http.Client{
		Timeout:       DownloadTimeout,
		CheckRedirect: checkArtifactRedirect,
		Transport:     newTransport(DownloadTimeout),
	}
}

func newTransport(responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return errRedirectRefused
}

// checkArtifactRedirect allows a bounded redirect chain that stays on HTTPS and
// on the pinned release hosts. Integrity does not rest on this check — every
// artifact is verified against the release checksums — but it keeps a hijacked
// redirect from turning the download into a request to an arbitrary host.
func checkArtifactRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", errRedirectNotAllowed, maxRedirects)
	}
	return requireArtifactURL(req.URL)
}

// requireArtifactURL accepts only a credential-free HTTPS URL on a pinned
// release host.
func requireArtifactURL(target *url.URL) error {
	if target == nil {
		return fmt.Errorf("%w: no URL", errRedirectNotAllowed)
	}
	if target.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not https", errRedirectNotAllowed, target.Scheme)
	}
	if target.User != nil {
		return fmt.Errorf("%w: URL carries credentials", errRedirectNotAllowed)
	}
	if !allowedArtifactHost(target.Hostname()) {
		return fmt.Errorf("%w: host %q is not a release host", errRedirectNotAllowed, target.Hostname())
	}
	return nil
}

func allowedArtifactHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	for _, allowed := range artifactRedirectHosts {
		if strings.HasPrefix(allowed, ".") {
			if strings.HasSuffix(host, allowed) && len(host) > len(allowed) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}
