package browser

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var alwaysBlockedHosts = map[string]bool{
	"metadata.google.internal": true,
	"metadata":                 true,
	"instance-data":            true,
	"169.254.169.254":          true,
	"100.100.100.200":          true,
	"fd00:ec2::254":            true,
}

var alwaysBlockedNets = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"),
	netip.MustParsePrefix("169.254.170.2/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

func CheckURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("unparseable URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if alwaysBlockedHosts[host] {
		return fmt.Errorf("blocked: %s is a cloud-metadata endpoint (always-blocked floor)", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if ipBlocked(addr) {
			return fmt.Errorf("blocked: %s is a cloud-metadata address (always-blocked floor)", host)
		}
		return nil
	}
	resolver := &net.Resolver{}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("blocked: DNS resolution for %s failed (%w) — fail-closed", host, err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if addr.Is4In6() {
			addr = addr.Unmap()
		}
		if ipBlocked(addr) {
			return fmt.Errorf("blocked: %s resolves to cloud-metadata address %s (always-blocked floor)", host, addr)
		}
	}
	return nil
}

func ipBlocked(addr netip.Addr) bool {
	for _, p := range alwaysBlockedNets {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func isPrivate(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return cgnat.Contains(addr)
}

var AllowPrivateURLs = false

func CheckPrivateURL(ctx context.Context, rawURL string) error {
	if AllowPrivateURLs {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("unparseable URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if addr, err := netip.ParseAddr(host); err == nil {
		if isPrivate(addr) {
			return fmt.Errorf("blocked: %s is a private/internal address (set browser.allowPrivateUrls to permit)", host)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := (&net.Resolver{}).LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("blocked: DNS resolution for %s failed (%w) — fail-closed", host, err)
	}
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok && isPrivate(addr) {
			return fmt.Errorf("blocked: %s resolves to private address %s (set browser.allowPrivateUrls to permit)", host, addr.Unmap())
		}
	}
	return nil
}
