package netx

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

// Proxy pool names with special meaning.
const (
	PoolAuto   = "auto"   // follow routing rules, then the default pool
	PoolDirect = "direct" // never use a proxy
)

// ProxyRoute sends hosts matching Match to a pool and tags them with a region.
// Match is a domain suffix such as ".ir" or "digikala.com", or "*" for everything.
type ProxyRoute struct {
	Match  string `yaml:"match" json:"match"`
	Pool   string `yaml:"pool" json:"pool"`
	Region string `yaml:"region" json:"region"`
}

// ProxyManager picks the outbound proxy for each request.
type ProxyManager struct {
	pools       map[string][]*url.URL
	routes      []ProxyRoute
	defaultPool string
	counter     uint64
}

// NewProxyManager validates pool URLs and routes.
func NewProxyManager(pools map[string][]string, routes []ProxyRoute, defaultPool string) (*ProxyManager, error) {
	m := &ProxyManager{pools: map[string][]*url.URL{}, defaultPool: strings.TrimSpace(defaultPool)}
	for name, list := range pools {
		name = strings.TrimSpace(name)
		if name == "" || name == PoolAuto || name == PoolDirect {
			return nil, fmt.Errorf("invalid proxy pool name %q", name)
		}
		parsed, err := ParseProxyList(list)
		if err != nil {
			return nil, fmt.Errorf("proxy pool %q: %w", name, err)
		}
		m.pools[name] = parsed
	}
	for _, r := range routes {
		r.Match = strings.ToLower(strings.TrimSpace(r.Match))
		r.Pool = strings.TrimSpace(r.Pool)
		if r.Match == "" {
			return nil, fmt.Errorf("proxy route with empty match")
		}
		if r.Pool != "" && r.Pool != PoolDirect {
			if _, ok := m.pools[r.Pool]; !ok {
				return nil, fmt.Errorf("proxy route %q references unknown pool %q", r.Match, r.Pool)
			}
		}
		m.routes = append(m.routes, r)
	}
	if m.defaultPool != "" && m.defaultPool != PoolDirect {
		if _, ok := m.pools[m.defaultPool]; !ok {
			return nil, fmt.Errorf("default proxy pool %q is not defined", m.defaultPool)
		}
	}
	return m, nil
}

// ParseProxyList parses proxy URLs (http, https, socks5, socks5h).
func ParseProxyList(list []string) ([]*url.URL, error) {
	var out []*url.URL
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q: %w", RedactProxy(raw), err)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q in %s", u.Scheme, RedactProxy(raw))
		}
		if u.Host == "" {
			return nil, fmt.Errorf("proxy %s has no host", RedactProxy(raw))
		}
		out = append(out, u)
	}
	return out, nil
}

// PoolNames returns the configured pool names, sorted.
func (m *ProxyManager) PoolNames() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.pools))
	for n := range m.pools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HasPool reports whether name is a configured pool or a special pool name.
func (m *ProxyManager) HasPool(name string) bool {
	if name == "" || name == PoolAuto || name == PoolDirect {
		return true
	}
	if m == nil {
		return false
	}
	_, ok := m.pools[name]
	return ok
}

// Routes returns a copy of the routing rules.
func (m *ProxyManager) Routes() []ProxyRoute {
	if m == nil {
		return []ProxyRoute{}
	}
	return append([]ProxyRoute{}, m.routes...)
}

func (m *ProxyManager) matchRoute(host string) (ProxyRoute, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, r := range m.routes {
		if r.Match == "*" {
			return r, true
		}
		suffix := strings.TrimPrefix(r.Match, ".")
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return r, true
		}
	}
	return ProxyRoute{}, false
}

// RegionFor returns the region tag of the first route matching host, or "".
func (m *ProxyManager) RegionFor(host string) string {
	if m == nil {
		return ""
	}
	if r, ok := m.matchRoute(host); ok {
		return r.Region
	}
	return ""
}

// Select returns the proxy for a request to host, or nil for a direct connection.
// custom proxies (set on the job) win over pools. pool is a pool name, PoolAuto
// (or "") to follow the routing rules, or PoolDirect. When stickyKey is non-empty
// the same key always maps to the same proxy, which keeps sessions on one exit IP.
func (m *ProxyManager) Select(host, pool string, custom []*url.URL, stickyKey string) *url.URL {
	list := custom
	if len(list) == 0 && m != nil {
		switch pool {
		case PoolDirect:
			return nil
		case "", PoolAuto:
			name := m.defaultPool
			if r, ok := m.matchRoute(host); ok {
				name = r.Pool
			}
			if name == PoolDirect {
				return nil
			}
			list = m.pools[name]
		default:
			list = m.pools[pool]
		}
	}
	if len(list) == 0 {
		return nil
	}
	if stickyKey != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(stickyKey))
		return list[int(h.Sum32()%uint32(len(list)))]
	}
	var n uint64
	if m != nil {
		n = atomic.AddUint64(&m.counter, 1)
	}
	return list[int(n%uint64(len(list)))]
}

// RedactProxy hides the password of a proxy URL for logs and the UI.
func RedactProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}
