package modules

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/resolver/crawler/config"
)

var (
	// ErrBadScanTarget means the input is not a domain or IP address.
	ErrBadScanTarget = errors.New("enter a domain name or IP address")
	// ErrResolveFailed means DNS lookup did not return an address.
	ErrResolveFailed = errors.New("could not resolve host")
)

// PortHit is one open port, including whatever the service revealed about itself.
type PortHit struct {
	Port       int
	Protocol   string
	Service    string
	Detail     string
	Group      string
	Banner     string
	Product    string
	Risk       string
	Importance int
	LatencyMS  int64
}

// PortScanner probes TCP ports and reads a short service banner.
type PortScanner struct {
	concurrent    int
	timeout       time.Duration
	bannerTimeout time.Duration
}

// NewPortScanner creates a scanner. Zero config values fall back to defaults.
func NewPortScanner(cfg config.PortScanConfig) *PortScanner {
	s := &PortScanner{
		concurrent:    cfg.Concurrent,
		timeout:       cfg.Timeout,
		bannerTimeout: cfg.BannerTimeout,
	}
	if s.concurrent < 1 {
		s.concurrent = 64
	}
	if s.timeout <= 0 {
		s.timeout = time.Second
	}
	if s.bannerTimeout <= 0 {
		s.bannerTimeout = 700 * time.Millisecond
	}
	return s
}

// NormalizeScanTarget accepts a domain, IP, or URL and returns the host to scan.
func NormalizeScanTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrBadScanTarget
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "https://"):
		raw = raw[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		raw = raw[len("http://"):]
	}
	if i := strings.IndexAny(raw, "/?"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	raw = strings.TrimSuffix(raw, ".")
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", ErrBadScanTarget
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else {
			host = strings.Trim(host, "[]")
		}
	} else if h, port, err := net.SplitHostPort(host); err == nil && port != "" {
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	host = strings.ToLower(host)
	if !validHostname(host) {
		return "", ErrBadScanTarget
	}
	return host, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// ResolveScanTarget returns the address to dial. IP literals are returned as-is.
// Hostnames prefer an IPv4 address when one exists.
func ResolveScanTarget(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("%w: %s", ErrResolveFailed, host)
	}
	for _, ip := range ips {
		if v4 := ip.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return ips[0].IP.String(), nil
}

// ScanPorts probes ports in the given order. onDone is called from one goroutine
// after each probe finishes. hit is nil when the port is closed or filtered.
// A cancelled context stops scheduling new probes and returns ctx.Err() unless
// every port was already probed.
func (s *PortScanner) ScanPorts(ctx context.Context, dialHost, nameHost string, ports []PortService, onDone func(scanned int, hit *PortHit)) error {
	if len(ports) == 0 {
		return nil
	}
	conc := s.concurrent
	if conc > len(ports) {
		conc = len(ports)
	}

	jobs := make(chan PortService)
	results := make(chan *PortHit, conc)
	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for svc := range jobs {
				if ctx.Err() != nil {
					continue
				}
				results <- s.probe(ctx, dialHost, nameHost, svc)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, svc := range ports {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- svc:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	scanned := 0
	for hit := range results {
		scanned++
		if onDone != nil {
			onDone(scanned, hit)
		}
	}
	if scanned >= len(ports) {
		return nil
	}
	return ctx.Err()
}

func (s *PortScanner) probe(ctx context.Context, dialHost, nameHost string, svc PortService) *PortHit {
	addr := net.JoinHostPort(dialHost, strconv.Itoa(svc.Port))
	start := time.Now()
	conn, err := s.dial(ctx, addr)
	if err != nil {
		return nil
	}
	defer conn.Close()

	hit := &PortHit{
		Port:       svc.Port,
		Protocol:   "tcp",
		Service:    svc.Service,
		Detail:     svc.Detail,
		Group:      svc.Group,
		Risk:       PortRisk(svc.Port),
		Importance: svc.Order,
		LatencyMS:  time.Since(start).Milliseconds(),
	}

	active := net.Conn(conn)
	prefix := ""

	if svc.TLS {
		_ = conn.SetDeadline(time.Now().Add(s.bannerTimeout))
		sni := nameHost
		if net.ParseIP(nameHost) != nil {
			sni = ""
		}
		tlsConn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         sni,
		})
		if err := tlsConn.Handshake(); err != nil {
			if !svc.HTTP {
				hit.Banner = "Open · TLS handshake failed"
				return hit
			}
			_ = tlsConn.Close()
			plain, derr := s.dial(ctx, addr)
			if derr != nil {
				hit.Banner = "Open · TLS handshake failed"
				return hit
			}
			defer plain.Close()
			active = plain
		} else {
			defer tlsConn.Close()
			active = tlsConn
			prefix = tlsSummary(tlsConn.ConnectionState())
		}
	}

	_ = active.SetDeadline(time.Now().Add(s.bannerTimeout))
	var raw []byte
	switch {
	case svc.HTTP:
		raw = httpProbe(active, nameHost)
	case svc.Port == 6379:
		raw = readAvailable(active, 512)
		if len(bytes.TrimSpace(raw)) == 0 {
			_ = active.SetDeadline(time.Now().Add(s.bannerTimeout))
			_, _ = io.WriteString(active, "PING\r\n")
			raw = readAvailable(active, 128)
		}
	case svc.Port == 11211:
		raw = readAvailable(active, 512)
		if len(bytes.TrimSpace(raw)) == 0 {
			_ = active.SetDeadline(time.Now().Add(s.bannerTimeout))
			_, _ = io.WriteString(active, "version\r\n")
			raw = readAvailable(active, 128)
		}
	default:
		raw = readAvailable(active, 512)
	}

	banner := describeBanner(svc, raw)
	if prefix != "" && banner != "" {
		banner = prefix + " · " + banner
	} else if prefix != "" {
		banner = prefix
	}
	hit.Banner = banner
	hit.Product = guessProduct(hit.Service, banner)
	return hit
}

func (s *PortScanner) dial(ctx context.Context, addr string) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return (&net.Dialer{Timeout: s.timeout}).DialContext(dctx, "tcp", addr)
}

func httpProbe(conn net.Conn, host string) []byte {
	_, _ = fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s\r\nUser-Agent: Mole/port-scan\r\nConnection: close\r\n\r\n", host)
	return readAvailable(conn, 2048)
}

func readAvailable(conn net.Conn, max int) []byte {
	buf := make([]byte, 0, max)
	tmp := make([]byte, 512)
	for len(buf) < max {
		n, err := conn.Read(tmp)
		if n > 0 {
			remain := max - len(buf)
			if n > remain {
				n = remain
			}
			buf = append(buf, tmp[:n]...)
			if bytes.Contains(buf, []byte("\r\n\r\n")) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return buf
}

func describeBanner(svc PortService, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if svc.HTTP || bytes.HasPrefix(bytes.ToUpper(raw), []byte("HTTP/")) {
		if summary := summarizeHTTP(raw); summary != "" {
			return summary
		}
	}
	if ver := mysqlVersion(raw); ver != "" && (svc.Port == 3306 || svc.Port == 3307) {
		return ver
	}
	return sanitizeBanner(string(raw))
}

func summarizeHTTP(raw []byte) string {
	text := string(raw)
	reader := bufio.NewReader(strings.NewReader(text))
	status, err := reader.ReadString('\n')
	if err != nil && len(status) == 0 {
		return ""
	}
	status = strings.TrimSpace(status)
	if !strings.HasPrefix(strings.ToUpper(status), "HTTP/") {
		return ""
	}
	parts := []string{status}
	if server := httpHeader(text, "Server"); server != "" {
		parts = append(parts, "Server: "+server)
	}
	if powered := httpHeader(text, "X-Powered-By"); powered != "" {
		parts = append(parts, powered)
	}
	if loc := httpHeader(text, "Location"); loc != "" {
		loc = sanitizeBanner(loc)
		if len([]rune(loc)) > 120 {
			loc = string([]rune(loc)[:120]) + "…"
		}
		parts = append(parts, "Location: "+loc)
	}
	return strings.Join(parts, " · ")
}

func httpHeader(raw, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func mysqlVersion(raw []byte) string {
	if len(raw) < 6 || raw[4] != 0x0a {
		return ""
	}
	rest := raw[5:]
	i := bytes.IndexByte(rest, 0)
	if i <= 0 {
		return ""
	}
	ver := sanitizeBanner(string(rest[:i]))
	if ver == "" {
		return ""
	}
	return "MySQL " + ver
}

func tlsSummary(state tls.ConnectionState) string {
	parts := []string{tlsVersionName(state.Version)}
	if len(state.PeerCertificates) > 0 {
		cert := state.PeerCertificates[0]
		name := cert.Subject.CommonName
		if name == "" && len(cert.DNSNames) > 0 {
			name = cert.DNSNames[0]
		}
		if name != "" {
			parts = append(parts, "CN="+sanitizeBanner(name))
		}
	}
	return strings.Join(parts, " · ")
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	default:
		return "TLS"
	}
}

func sanitizeBanner(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	rs := []rune(out)
	if len(rs) > 240 {
		out = string(rs[:240]) + "…"
	}
	return out
}

func guessProduct(service, banner string) string {
	if strings.TrimSpace(banner) == "" {
		return ""
	}
	b := strings.ToLower(banner)
	switch {
	case strings.Contains(b, "openssh"):
		return "OpenSSH"
	case strings.Contains(b, "nginx"):
		return "nginx"
	case strings.Contains(b, "apache"):
		return "Apache"
	case strings.Contains(b, "microsoft-iis"), strings.Contains(b, "microsoft httpapi"):
		return "IIS"
	case strings.Contains(b, "cloudflare"):
		return "Cloudflare"
	case strings.Contains(b, "openresty"):
		return "OpenResty"
	case strings.Contains(b, "caddy"):
		return "Caddy"
	case strings.Contains(b, "litespeed"):
		return "LiteSpeed"
	case strings.Contains(b, "tomcat"), strings.Contains(b, "coyote"):
		return "Tomcat"
	case strings.Contains(b, "jetty"):
		return "Jetty"
	case strings.Contains(b, "weblogic"):
		return "WebLogic"
	case strings.Contains(b, "gunicorn"):
		return "Gunicorn"
	case strings.Contains(b, "uvicorn"):
		return "Uvicorn"
	case strings.Contains(b, "werkzeug"):
		return "Werkzeug"
	case strings.Contains(b, "kestrel"):
		return "Kestrel"
	case strings.Contains(b, "express"):
		return "Express"
	case strings.Contains(b, "envoy"):
		return "Envoy"
	case strings.Contains(b, "haproxy"):
		return "HAProxy"
	case strings.Contains(b, "varnish"):
		return "Varnish"
	case strings.Contains(b, "squid"):
		return "Squid"
	case strings.Contains(b, "traefik"):
		return "Traefik"
	case strings.Contains(b, "proftpd"):
		return "ProFTPD"
	case strings.Contains(b, "vsftpd"):
		return "vsftpd"
	case strings.Contains(b, "pure-ftpd"):
		return "Pure-FTPd"
	case strings.Contains(b, "postfix"):
		return "Postfix"
	case strings.Contains(b, "exim"):
		return "Exim"
	case strings.Contains(b, "sendmail"):
		return "Sendmail"
	case strings.Contains(b, "dovecot"):
		return "Dovecot"
	case strings.Contains(b, "elasticsearch"):
		return "Elasticsearch"
	case strings.Contains(b, "kibana"):
		return "Kibana"
	case strings.Contains(b, "grafana"):
		return "Grafana"
	case strings.Contains(b, "prometheus"):
		return "Prometheus"
	case strings.Contains(b, "consul"):
		return "Consul"
	case strings.Contains(b, "vault"):
		return "Vault"
	case strings.Contains(b, "rabbitmq"):
		return "RabbitMQ"
	case strings.Contains(b, "minio"):
		return "MinIO"
	case strings.Contains(b, "mysql"), strings.HasPrefix(b, "mysql "):
		return "MySQL"
	case strings.Contains(b, "mariadb"):
		return "MariaDB"
	case strings.Contains(b, "redis"), strings.Contains(b, "+pong"):
		return "Redis"
	case strings.Contains(b, "mongodb"):
		return "MongoDB"
	case strings.Contains(b, "postgresql"):
		return "PostgreSQL"
	case strings.Contains(b, "clickhouse"):
		return "ClickHouse"
	case strings.Contains(b, "memcached") || (service == "Memcached" && strings.HasPrefix(b, "version ")):
		return "Memcached"
	}
	return ""
}
