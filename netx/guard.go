// Package netx holds the network layer shared by every outbound request the
// engine makes: SSRF protection, proxy selection, TLS fingerprinting and
// per-host rate limiting.
package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlockedAddress is returned when a request targets an address the guard does not allow.
var ErrBlockedAddress = errors.New("blocked: private network address")

// blockedNets are ranges that must never be reachable from user-supplied URLs:
// loopback, RFC1918, link-local (cloud metadata), CGNAT, multicast, reserved, etc.
var blockedNets = mustParseCIDRs(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"::/128",
	"::1/128",
	"64:ff9b:1::/48",
	"100::/64",
	"2001:db8::/32",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

var nat64Net = mustParseCIDRs("64:ff9b::/96")[0]

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// Guard decides which destination addresses outbound requests may reach.
// The zero value is not usable; create one with NewGuard.
type Guard struct {
	allowPrivate bool
	allowed      []*net.IPNet
	resolver     *net.Resolver
}

// NewGuard creates a guard. When allowPrivate is true every address is allowed
// (useful for a local, single-user install). allowedCIDRs are exceptions that are
// always reachable even when private addresses are blocked.
func NewGuard(allowPrivate bool, allowedCIDRs []string) (*Guard, error) {
	g := &Guard{allowPrivate: allowPrivate, resolver: net.DefaultResolver}
	for _, c := range allowedCIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil && ip.To4() != nil {
				c += "/32"
			} else {
				c += "/128"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed CIDR %q: %w", c, err)
		}
		g.allowed = append(g.allowed, n)
	}
	return g, nil
}

// AllowsPrivate reports whether the guard lets requests reach private networks.
func (g *Guard) AllowsPrivate() bool {
	return g == nil || g.allowPrivate
}

// IPAllowed reports whether ip may be contacted.
func (g *Guard) IPAllowed(ip net.IP) bool {
	if g == nil || g.allowPrivate {
		return true
	}
	if ip == nil {
		return false
	}
	for _, n := range g.allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return !IsPrivateIP(ip)
}

// IsPrivateIP reports whether ip is in a loopback, private, link-local,
// multicast, reserved or otherwise non-public range.
func IsPrivateIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if nat64Net.Contains(ip) {
		// NAT64 addresses embed an IPv4 address in the last four bytes.
		return IsPrivateIP(net.IP(ip[12:16]))
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckHost resolves host and returns ErrBlockedAddress when any of its
// addresses is not allowed.
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	if g == nil || g.allowPrivate {
		return nil
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return ErrBlockedAddress
		}
		return nil
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return ErrBlockedAddress
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, a := range addrs {
		if !g.IPAllowed(a.IP) {
			return ErrBlockedAddress
		}
	}
	return nil
}

// CheckURL validates that rawURL is an http(s) URL whose host is allowed.
func (g *Guard) CheckURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("URLs with credentials are not allowed")
	}
	return g.CheckHost(ctx, u.Hostname())
}

// Control is a net.Dialer Control hook that rejects connections to disallowed
// addresses after DNS resolution, which closes the DNS-rebinding gap left by
// checking the hostname before dialing.
func (g *Guard) Control(network, address string, _ syscall.RawConn) error {
	if g == nil || g.allowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !g.IPAllowed(ip) {
		return ErrBlockedAddress
	}
	return nil
}
