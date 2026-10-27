package netx

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// TLS fingerprints a Transport can present. FingerprintGo uses Go's own TLS
// stack; the others replay a real browser's ClientHello (JA3/JA4) via uTLS.
const (
	FingerprintGo      = "go"
	FingerprintChrome  = "chrome"
	FingerprintFirefox = "firefox"
	FingerprintSafari  = "safari"
	FingerprintEdge    = "edge"
	FingerprintIOS     = "ios"
)

// Fingerprints lists the supported fingerprint names.
var Fingerprints = []string{FingerprintChrome, FingerprintFirefox, FingerprintSafari, FingerprintEdge, FingerprintIOS, FingerprintGo}

func helloFor(name string) (utls.ClientHelloID, bool) {
	switch name {
	case FingerprintChrome, "":
		return utls.HelloChrome_Auto, true
	case FingerprintFirefox:
		return utls.HelloFirefox_Auto, true
	case FingerprintSafari:
		return utls.HelloSafari_Auto, true
	case FingerprintEdge:
		return utls.HelloEdge_Auto, true
	case FingerprintIOS:
		return utls.HelloIOS_Auto, true
	}
	return utls.ClientHelloID{}, false
}

// ValidFingerprint reports whether name is a supported fingerprint.
func ValidFingerprint(name string) bool {
	for _, f := range Fingerprints {
		if f == name {
			return true
		}
	}
	return false
}

// TransportOptions configures a Transport.
type TransportOptions struct {
	Fingerprint        string
	Proxy              *url.URL // nil = direct
	Guard              *Guard   // applied to direct connections
	DialTimeout        time.Duration
	TLSTimeout         time.Duration
	ResponseTimeout    time.Duration
	InsecureSkipVerify bool
}

// Transport is an http.RoundTripper that can present a browser TLS fingerprint
// over HTTP/1.1 and HTTP/2, directly or through an HTTP(S)/SOCKS5 proxy.
type Transport struct {
	opts    TransportOptions
	hello   utls.ClientHelloID
	useUTLS bool
	std     *http.Transport // FingerprintGo
	h1      *http.Transport // uTLS, HTTP/1.1
	h2      *http2.Transport
	h1Hosts sync.Map // host:port of servers that did not negotiate h2
}

var errNotH2 = errors.New("netx: server did not negotiate h2")

// NewTransport builds a Transport.
func NewTransport(opts TransportOptions) *Transport {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.TLSTimeout <= 0 {
		opts.TLSTimeout = 15 * time.Second
	}
	if opts.ResponseTimeout <= 0 {
		opts.ResponseTimeout = 30 * time.Second
	}
	t := &Transport{opts: opts}
	t.hello, t.useUTLS = helloFor(opts.Fingerprint)

	dialCtx := t.dialDirectOrPlain
	if !t.useUTLS {
		t.std = &http.Transport{
			Proxy:                 t.proxyFunc(false),
			DialContext:           dialCtx,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   opts.TLSTimeout,
			ResponseHeaderTimeout: opts.ResponseTimeout,
			ExpectContinueTimeout: time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: opts.InsecureSkipVerify,
			},
		}
		return t
	}

	t.h1 = &http.Transport{
		// Plain-HTTP requests go through the proxy the standard way; HTTPS
		// requests use DialTLSContext, which tunnels through the proxy itself.
		Proxy:       t.proxyFunc(true),
		DialContext: dialCtx,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return t.dialUTLS(ctx, addr, true)
		},
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: opts.ResponseTimeout,
		ExpectContinueTimeout: time.Second,
	}
	t.h2 = &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			conn, err := t.dialUTLS(ctx, addr, false)
			if err != nil {
				return nil, err
			}
			if conn.ConnectionState().NegotiatedProtocol != "h2" {
				t.h1Hosts.Store(addr, true)
				conn.Close()
				return nil, errNotH2
			}
			return conn, nil
		},
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     15 * time.Second,
	}
	return t
}

// proxyFunc returns the http.Transport Proxy hook. When httpOnly is set, the
// proxy is used for plain-HTTP requests only.
func (t *Transport) proxyFunc(httpOnly bool) func(*http.Request) (*url.URL, error) {
	p := t.opts.Proxy
	if p == nil {
		return nil
	}
	return func(req *http.Request) (*url.URL, error) {
		if httpOnly && req.URL.Scheme != "http" {
			return nil, nil
		}
		return p, nil
	}
}

// dialDirectOrPlain dials the target directly (guarded) when no proxy is set,
// or the proxy itself (unguarded; proxies are operator-configured) otherwise.
func (t *Transport) dialDirectOrPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: t.opts.DialTimeout, KeepAlive: 30 * time.Second}
	if t.opts.Proxy == nil && t.opts.Guard != nil {
		d.Control = t.opts.Guard.Control
	}
	return d.DialContext(ctx, network, addr)
}

// dialTarget opens a raw TCP stream to addr, through the proxy when one is set.
func (t *Transport) dialTarget(ctx context.Context, addr string) (net.Conn, error) {
	p := t.opts.Proxy
	if p == nil {
		return t.dialDirectOrPlain(ctx, "tcp", addr)
	}
	switch p.Scheme {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if p.User != nil {
			pw, _ := p.User.Password()
			auth = &proxy.Auth{User: p.User.Username(), Password: pw}
		}
		sd, err := proxy.SOCKS5("tcp", hostPortDefault(p.Host, "1080"), auth, &net.Dialer{Timeout: t.opts.DialTimeout})
		if err != nil {
			return nil, err
		}
		if cd, ok := sd.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return sd.Dial("tcp", addr)
	default:
		return t.connectTunnel(ctx, addr)
	}
}

// connectTunnel opens an HTTP CONNECT tunnel to addr through an HTTP(S) proxy.
func (t *Transport) connectTunnel(ctx context.Context, addr string) (net.Conn, error) {
	p := t.opts.Proxy
	defPort := "80"
	if p.Scheme == "https" {
		defPort = "443"
	}
	conn, err := (&net.Dialer{Timeout: t.opts.DialTimeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", hostPortDefault(p.Host, defPort))
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", p.Host, err)
	}
	deadline := time.Now().Add(t.opts.DialTimeout + t.opts.TLSTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if p.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: p.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy TLS: %w", err)
		}
		conn = tc
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: http.Header{},
	}
	if p.User != nil {
		pw, _ := p.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(p.User.Username() + ":" + pw))
		req.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT response: %w", err)
	}
	// Like net/http, never touch resp.Body: a 2xx CONNECT response has no body
	// and draining it would block on the tunnel.
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s: %s", addr, resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// dialUTLS opens a TLS connection presenting the configured browser
// fingerprint. h1Only rewrites the ALPN extension to offer only http/1.1 so the
// connection can be handed to net/http's HTTP/1.1 transport.
func (t *Transport) dialUTLS(ctx context.Context, addr string, h1Only bool) (*utls.UConn, error) {
	raw, err := t.dialTarget(ctx, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		raw.Close()
		return nil, err
	}
	cfg := &utls.Config{ServerName: host, InsecureSkipVerify: t.opts.InsecureSkipVerify}
	var uc *utls.UConn
	if h1Only {
		spec, err := utls.UTLSIdToSpec(t.hello)
		if err != nil {
			raw.Close()
			return nil, err
		}
		for _, ext := range spec.Extensions {
			if alpn, ok := ext.(*utls.ALPNExtension); ok {
				alpn.AlpnProtocols = []string{"http/1.1"}
			}
		}
		uc = utls.UClient(raw, cfg, utls.HelloCustom)
		if err := uc.ApplyPreset(&spec); err != nil {
			raw.Close()
			return nil, err
		}
	} else {
		uc = utls.UClient(raw, cfg, t.hello)
	}
	hctx, cancel := context.WithTimeout(ctx, t.opts.TLSTimeout)
	defer cancel()
	if err := uc.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("tls handshake with %s: %w", host, err)
	}
	return uc, nil
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.useUTLS {
		return t.std.RoundTrip(req)
	}
	if req.URL.Scheme != "https" {
		return t.h1.RoundTrip(req)
	}
	key := hostPortDefault(req.URL.Host, "443")
	if _, h1 := t.h1Hosts.Load(key); h1 {
		return t.h1.RoundTrip(req)
	}
	resp, err := t.h2.RoundTrip(req)
	if err == nil || !(errors.Is(err, errNotH2) || strings.Contains(err.Error(), errNotH2.Error())) {
		return resp, err
	}
	// The server only speaks HTTP/1.1: replay the request over the h1 transport.
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, fmt.Errorf("server requires HTTP/1.1 and the request body cannot be replayed")
		}
		body, gerr := req.GetBody()
		if gerr != nil {
			return nil, gerr
		}
		req = req.Clone(req.Context())
		req.Body = body
	}
	return t.h1.RoundTrip(req)
}

// CloseIdleConnections closes idle connections in every underlying transport.
func (t *Transport) CloseIdleConnections() {
	if t.std != nil {
		t.std.CloseIdleConnections()
	}
	if t.h1 != nil {
		t.h1.CloseIdleConnections()
	}
	if t.h2 != nil {
		t.h2.CloseIdleConnections()
	}
}

func hostPortDefault(hostport, port string) string {
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		return hostport
	}
	return net.JoinHostPort(strings.Trim(hostport, "[]"), port)
}

// TransportPool shares Transports (and so their connection pools) between jobs
// that use the same fingerprint and proxy.
type TransportPool struct {
	mu    sync.Mutex
	guard *Guard
	base  TransportOptions
	items map[string]*Transport
}

// NewTransportPool creates a pool; base supplies timeouts and TLS settings.
func NewTransportPool(guard *Guard, base TransportOptions) *TransportPool {
	return &TransportPool{guard: guard, base: base, items: map[string]*Transport{}}
}

// Get returns the shared Transport for a fingerprint/proxy pair.
func (p *TransportPool) Get(fingerprint string, proxyURL *url.URL) *Transport {
	if !ValidFingerprint(fingerprint) {
		fingerprint = FingerprintChrome
	}
	key := fingerprint + "|"
	if proxyURL != nil {
		key += proxyURL.String()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.items[key]; ok {
		return t
	}
	opts := p.base
	opts.Fingerprint = fingerprint
	opts.Proxy = proxyURL
	opts.Guard = p.guard
	t := NewTransport(opts)
	p.items[key] = t
	return t
}

// CloseIdle closes idle connections in every pooled Transport.
func (p *TransportPool) CloseIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.items {
		t.CloseIdleConnections()
	}
}
