package netx

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":        true,
		"10.1.2.3":         true,
		"172.20.0.1":       true,
		"192.168.1.1":      true,
		"169.254.169.254":  true,
		"100.64.0.1":       true,
		"0.0.0.0":          true,
		"::1":              true,
		"fe80::1":          true,
		"fd00::1":          true,
		"::ffff:127.0.0.1": true,
		"64:ff9b::7f00:1":  true,
		"8.8.8.8":          false,
		"185.143.233.1":    false,
		"2606:4700::1111":  false,
	}
	for ip, want := range cases {
		if got := IsPrivateIP(net.ParseIP(ip)); got != want {
			t.Errorf("IsPrivateIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestGuardCheckURL(t *testing.T) {
	g, err := NewGuard(false, []string{"10.9.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, u := range []string{"http://127.0.0.1/", "http://localhost:8080/x", "http://[::1]/", "http://169.254.169.254/latest", "http://foo.localhost/"} {
		if err := g.CheckURL(ctx, u); err == nil {
			t.Errorf("CheckURL(%s) allowed, want blocked", u)
		}
	}
	if err := g.CheckURL(ctx, "http://10.9.1.1/"); err != nil {
		t.Errorf("allowlisted CIDR blocked: %v", err)
	}
	if err := g.CheckURL(ctx, "ftp://example.com/"); err == nil {
		t.Error("ftp scheme allowed")
	}
	if err := g.CheckURL(ctx, "http://user:pw@8.8.8.8/"); err == nil {
		t.Error("credentials in URL allowed")
	}
	open, _ := NewGuard(true, nil)
	if err := open.CheckURL(ctx, "http://127.0.0.1/"); err != nil {
		t.Errorf("allowPrivate guard blocked loopback: %v", err)
	}
}

func TestGuardBlocksDialAfterResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	g, _ := NewGuard(false, nil)
	tr := NewTransport(TransportOptions{Fingerprint: FingerprintGo, Guard: g})
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("request to loopback succeeded through guarded transport")
	}
	if !strings.Contains(err.Error(), ErrBlockedAddress.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProxySelect(t *testing.T) {
	m, err := NewProxyManager(map[string][]string{
		"ir":   {"http://u:p@1.1.1.1:8080", "socks5://2.2.2.2:1080"},
		"intl": {"http://3.3.3.3:3128"},
	}, []ProxyRoute{{Match: ".ir", Pool: "ir", Region: "ir"}, {Match: "direct.com", Pool: PoolDirect}}, "intl")
	if err != nil {
		t.Fatal(err)
	}
	if p := m.Select("www.digikala.ir", "", nil, ""); p == nil || (p.Host != "1.1.1.1:8080" && p.Host != "2.2.2.2:1080") {
		t.Fatalf(".ir route not used: %v", p)
	}
	if p := m.Select("example.com", PoolAuto, nil, ""); p == nil || p.Host != "3.3.3.3:3128" {
		t.Fatalf("default pool not used: %v", p)
	}
	if p := m.Select("a.direct.com", "", nil, ""); p != nil {
		t.Fatalf("direct route returned proxy %v", p)
	}
	if p := m.Select("example.com", PoolDirect, nil, ""); p != nil {
		t.Fatalf("direct pool returned proxy %v", p)
	}
	a := m.Select("x.ir", "ir", nil, "session-1")
	for i := 0; i < 10; i++ {
		if b := m.Select("x.ir", "ir", nil, "session-1"); b != a {
			t.Fatal("sticky selection changed")
		}
	}
	if m.RegionFor("shop.example.ir") != "ir" || m.RegionFor("example.com") != "" {
		t.Fatal("RegionFor mismatch")
	}
	custom, _ := ParseProxyList([]string{"http://9.9.9.9:1"})
	if p := m.Select("x.ir", "ir", custom, ""); p.Host != "9.9.9.9:1" {
		t.Fatal("custom proxies must win")
	}
	if _, err := NewProxyManager(nil, []ProxyRoute{{Match: ".ir", Pool: "missing"}}, ""); err == nil {
		t.Fatal("unknown pool accepted")
	}
	if RedactProxy("http://u:secret@h:1") != "http://u:xxxxx@h:1" {
		t.Fatalf("redact: %s", RedactProxy("http://u:secret@h:1"))
	}
}

func TestHostLimiter(t *testing.T) {
	l := NewHostLimiter(20) // 50ms spacing
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.Wait(ctx, "a.com", 0); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 140*time.Millisecond {
		t.Fatalf("4 requests took %v, want >= 150ms", el)
	}
	start = time.Now()
	_ = l.Wait(ctx, "b.com", 0)
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("other host should not wait")
	}
	l.Penalize("c.com", 80*time.Millisecond)
	start = time.Now()
	_ = l.Wait(ctx, "c.com", 0)
	if time.Since(start) < 70*time.Millisecond {
		t.Fatal("penalty not applied")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	l.Penalize("d.com", time.Second)
	if err := l.Wait(cctx, "d.com", 0); err == nil {
		t.Fatal("cancelled wait returned nil")
	}
}

// connectProxy is a minimal HTTP CONNECT proxy for tests.
func connectProxy(t *testing.T, used *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			// Plain-HTTP forward proxying.
			atomic.AddInt32(used, 1)
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		if r.Header.Get("Proxy-Authorization") == "" {
			http.Error(w, "auth", http.StatusProxyAuthRequired)
			return
		}
		atomic.AddInt32(used, 1)
		dst, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		hj, _ := w.(http.Hijacker)
		src, _, err := hj.Hijack()
		if err != nil {
			dst.Close()
			return
		}
		io.WriteString(src, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(dst, src); dst.Close() }()
		go func() { io.Copy(src, dst); src.Close() }()
	}))
}

func TestUTLSThroughConnectProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "proto="+r.Proto)
	}))
	defer target.Close()
	var used int32
	px := connectProxy(t, &used)
	defer px.Close()
	pu, _ := url.Parse(px.URL)
	pu.User = url.UserPassword("user", "pass")

	for _, fp := range []string{FingerprintChrome, FingerprintFirefox, FingerprintGo} {
		tr := NewTransport(TransportOptions{Fingerprint: fp, Proxy: pu, InsecureSkipVerify: true})
		resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get(target.URL + "/x")
		if err != nil {
			t.Fatalf("%s: %v", fp, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.HasPrefix(string(b), "proto=HTTP/") {
			t.Fatalf("%s: body %q", fp, b)
		}
	}
	if atomic.LoadInt32(&used) < 3 {
		t.Fatalf("proxy used %d times, want >= 3", used)
	}
}

func TestUTLSDirectH1Fallback(t *testing.T) {
	// httptest TLS servers only speak HTTP/1.1, so the h2 attempt must fall back.
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, r.Proto+" "+string(b))
	}))
	defer target.Close()
	tr := NewTransport(TransportOptions{Fingerprint: FingerprintChrome, InsecureSkipVerify: true})
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	for i := 0; i < 2; i++ {
		resp, err := c.Post(target.URL, "text/plain", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "HTTP/1.1 hello" {
			t.Fatalf("got %q", b)
		}
	}
}

// TestLiveFingerprint hits a public TLS echo service. Run with MOLE_NET_TESTS=1.
func TestLiveFingerprint(t *testing.T) {
	if os.Getenv("MOLE_NET_TESTS") == "" {
		t.Skip("set MOLE_NET_TESTS=1 to run live network tests")
	}
	g, _ := NewGuard(false, nil)
	tr := NewTransport(TransportOptions{Fingerprint: FingerprintChrome, Guard: g})
	req, _ := http.NewRequest("GET", "https://tls.peet.ws/api/clean", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	t.Logf("proto=%s response=%v", resp.Proto, out)
	if resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", resp.Proto)
	}
}
