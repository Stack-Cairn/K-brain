package imaging

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
	"golang.org/x/net/idna"
)

// Public image output retrieval never inherits provider credentials, cookies or the
// provider HTTP client. A direct connection resolves and pins a validated public
// address, including connections following redirects.
func publicImageAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "168.63.129.16/32", "192.88.99.0/24", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	return true
}

func validImageOutputURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(u.String()) > 8192 {
		return false
	}
	if u.Port() != "" && u.Port() != "443" {
		return false
	}
	// Judge the name net/http will actually send: it folds full-width digits and dots the
	// same way, and a proxy may resolve whatever spelling reaches it.
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil {
		if ip, ipErr := netip.ParseAddr(u.Hostname()); ipErr == nil {
			return publicImageAddress(ip)
		}
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || strings.HasSuffix(host, ".") {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicImageAddress(ip)
	}
	if numericHostLabel(host[strings.LastIndexByte(host, '.')+1:]) {
		return false
	}
	return !strings.ContainsAny(host, "%\\\r\n")
}

// numericHostLabel catches 2130706433, 0x7f.0.0.1, 0177.0.0.1 and 127.1: resolvers read these
// as IP addresses, and no public top-level domain looks like this.
func numericHostLabel(label string) bool {
	if strings.HasPrefix(label, "0x") {
		return true
	}
	return label != "" && strings.Trim(label, "0123456789") == ""
}

func resolvePublicImageAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, &ImageError{Code: CodeUnavailable, Reason: "image host could not be resolved"}
	}
	for _, ip := range ips {
		if !publicImageAddress(ip) {
			return nil, &ImageError{Code: CodeForbidden, Reason: "image host resolved to a non-public address"}
		}
	}
	return ips, nil
}

func imageOutputTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	return &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives: true, DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != strconv.Itoa(443) {
				return nil, &ImageError{Code: CodeForbidden}
			}
			ips, err := resolvePublicImageAddresses(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				if ctx.Err() != nil {
					break
				}
			}
			return nil, &ImageError{Code: CodeUnavailable, Reason: "could not connect to the image host"}
		},
	}
}

const (
	imageDownloadTimeout = 5 * time.Minute
	minDownloadSpeed     = 100 << 10 // bytes per second
	// reasonSlowDirectDownload is matched by clients, which then suggest the connection's proxy
	// toggle. Keep it stable.
	reasonSlowDirectDownload = "image download was too slow over a direct connection"
	reasonDownloadTimedOut   = "image download timed out"
)

var directOutputRoute = func() outputRoute { return imageOutputTransport() }

type outputRoute interface {
	http.RoundTripper
	CloseIdleConnections()
}

func downloadImageOutput(ctx context.Context, source OutputSource) (ImageContent, error) {
	u, err := url.Parse(source.URL)
	if err != nil || !validImageOutputURL(u) {
		return ImageContent{}, &ImageError{Code: CodeForbidden, Reason: "provider returned an image address that is not allowed"}
	}
	// Routing follows the connection's toggle and nothing else. A slow direct download keeps
	// going, since it may still finish, and a failure tells the user about the toggle.
	if !source.UseProxy {
		return retrieveWithin(ctx, source, directOutputRoute())
	}
	proxy, err := netproxy.ProxyFor(u)
	if err != nil {
		return ImageContent{}, &ImageError{Code: CodeUnavailable, Reason: "app proxy configuration is invalid"}
	}
	if proxy != nil {
		return retrieveWithin(ctx, source, proxiedImageOutputTransport(proxy))
	}
	return retrieveWithin(ctx, source, directOutputRoute())
}

func retrieveWithin(ctx context.Context, source OutputSource, route outputRoute) (ImageContent, error) {
	defer route.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, imageDownloadTimeout)
	defer cancel()
	content, err := retrieveImageOutput(ctx, source, route)
	var image *ImageError
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.As(err, &image) {
		return content, downloadBudgetSpent{image}
	}
	return content, err
}

// proxiedImageOutputTransport serves connections whose user routes provider traffic through
// the app proxy. The proxy resolves the host, so
// the resolved-address pinning cannot apply (a fake-IP proxy would fail it anyway). URL
// validation, the redirect limit and the absence of credentials stay in force.
func proxiedImageOutputTransport(proxy *url.URL) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives: true, DisableCompression: true,
	}
}

// The internal transport seam permits deterministic tests; production callers
// always supply imageOutputTransport, never a provider or browser client.
func retrieveImageOutput(ctx context.Context, source OutputSource, transport http.RoundTripper) (ImageContent, error) {
	u, err := url.Parse(source.URL)
	if err != nil || !validImageOutputURL(u) {
		return ImageContent{}, &ImageError{Code: CodeForbidden}
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !validImageOutputURL(req.URL) {
			return &ImageError{Code: CodeForbidden}
		}
		req.Header = make(http.Header)
		req.Header.Set("Accept", "image/png,image/jpeg,image/webp")
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ImageContent{}, &ImageError{Code: CodeForbidden}
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp")
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ImageContent{}, &ImageError{Code: CodeUnavailable, Reason: downloadFailure(source, err, 0, started)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ImageContent{}, &ImageError{Code: CodeUnavailable, Reason: "image host returned HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	if resp.ContentLength > OutputImageLimits.MaxBytes {
		return ImageContent{}, &ImageError{Code: CodeTooLarge}
	}
	// DecodeImageContent applies byte and pixel limits and validates actual bytes.
	mime := source.MIME
	if mime == "" {
		mime = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		return ImageContent{}, &ImageError{Code: CodeInvalidInput}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, OutputImageLimits.MaxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return ImageContent{}, &ImageError{Code: CodeUnavailable, Reason: downloadFailure(source, err, int64(len(raw)), started)}
	}
	content, err := DecodeImageContent(ctx, bytes.NewReader(raw), mime, OutputImageLimits)
	if err != nil && ctx.Err() != nil {
		return ImageContent{}, &ImageError{Code: CodeUnavailable, Reason: downloadFailureReason(ctx.Err())}
	}
	return content, err
}

// downloadFailure names a direct route as the cause only for a timeout at low speed: a refused
// connection or an HTTP error would not improve through the proxy toggle, and a connection
// already on the proxy has no toggle left to suggest.
func downloadFailure(source OutputSource, err error, received int64, started time.Time) string {
	reason := downloadFailureReason(err)
	if reason == reasonDownloadTimedOut && !source.UseProxy && float64(received)/time.Since(started).Seconds() < minDownloadSpeed {
		return reasonSlowDirectDownload
	}
	return reason
}

func slowDirectDownload(err error) bool {
	var ie *ImageError
	return errors.As(err, &ie) && ie.Reason == reasonSlowDirectDownload
}

// downloadBudgetSpent marks a download that used its whole time budget, on either route. A retry
// would only repeat it at the same speed. A brief stall before the headers is not this: it can
// clear up.
type downloadBudgetSpent struct{ *ImageError }

func (e downloadBudgetSpent) Unwrap() error { return e.ImageError }

// downloadFailureReason uses fixed phrases: a transport error embeds the signed output URL.
func downloadFailureReason(err error) string {
	var ie *ImageError
	if errors.As(err, &ie) && ie.Reason != "" {
		return ie.Reason
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return reasonDownloadTimedOut
	}
	return "image download failed"
}
