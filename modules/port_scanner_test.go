package modules

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/config"
)

func TestKnownPortsUniqueAndOrdered(t *testing.T) {
	ports := KnownPorts()
	if len(ports) < 150 {
		t.Fatalf("catalog too small: %d", len(ports))
	}
	seen := map[int]string{}
	for i, p := range ports {
		if p.Port < 1 || p.Port > 65535 {
			t.Fatalf("bad port %d", p.Port)
		}
		if p.Service == "" || p.Group == "" {
			t.Fatalf("port %d missing label", p.Port)
		}
		if p.Order != i+1 {
			t.Fatalf("order %d at index %d", p.Order, i)
		}
		if prev, ok := seen[p.Port]; ok {
			t.Fatalf("duplicate port %d (%s and %s)", p.Port, prev, p.Service)
		}
		seen[p.Port] = p.Service
	}
	if ports[0].Port != 22 || ports[0].Service != "SSH" {
		t.Fatalf("first port = %+v", ports[0])
	}
	index := func(port int) int {
		for i, p := range ports {
			if p.Port == port {
				return i
			}
		}
		return -1
	}
	if index(22) > index(80) || index(80) > index(443) || index(443) > index(25565) {
		t.Fatal("important ports are not scanned before the long tail")
	}
	for _, must := range []int{22, 80, 443, 3389, 3306, 5432, 6379, 27017, 9200, 6443, 502} {
		if index(must) < 0 {
			t.Fatalf("missing port %d", must)
		}
	}
}

func TestPortRiskFlagsDangerousPortsOnly(t *testing.T) {
	if PortRisk(443) != "" || PortRisk(80) != "" || PortRisk(993) != "" || PortRisk(587) != "" {
		t.Fatal("ordinary web and mail-submission ports should not be flagged")
	}
	for _, port := range []int{22, 23, 445, 3389, 6379, 2375, 502, 21} {
		if PortRisk(port) == "" {
			t.Fatalf("port %d should be flagged", port)
		}
	}
	known := map[int]bool{}
	for _, p := range KnownPorts() {
		known[p.Port] = true
	}
	for port := range portRisks {
		if !known[port] {
			t.Fatalf("risk text for port %d has no catalog entry", port)
		}
	}
}

func TestNormalizeScanTarget(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"Example.COM", "example.com"},
		{"https://Example.COM/path?q=1", "example.com"},
		{"http://example.com:8443/a", "example.com"},
		{"  192.168.1.10 ", "192.168.1.10"},
		{"https://192.168.0.1/admin", "192.168.0.1"},
		{"[::1]", "::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"example.com.", "example.com"},
		{"user@example.com", "example.com"},
	}
	for _, tc := range cases {
		got, err := NormalizeScanTarget(tc.in)
		if err != nil || got != tc.out {
			t.Fatalf("%q -> %q, %v; want %q", tc.in, got, err, tc.out)
		}
	}
	for _, bad := range []string{"", "not a host", "http://", "exa mple.com", "-bad.com"} {
		if _, err := NormalizeScanTarget(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestResolveIPLiteral(t *testing.T) {
	ip, err := ResolveScanTarget(context.Background(), "127.0.0.1")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("got %q %v", ip, err)
	}
}

func TestScanPortsFindsOpenHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 2048)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("HTTP/1.0 200 OK\r\nServer: TestServer\r\n\r\n"))
	}()

	openPort := ln.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	scanner := NewPortScanner(config.PortScanConfig{
		Concurrent:    2,
		Timeout:       500 * time.Millisecond,
		BannerTimeout: time.Second,
	})
	ports := []PortService{
		{Port: closedPort, Service: "Closed", Group: GroupWeb, Order: 1},
		{Port: openPort, Service: "HTTP", Group: GroupWeb, HTTP: true, Order: 2},
	}
	var found *PortHit
	err = scanner.ScanPorts(context.Background(), "127.0.0.1", "127.0.0.1", ports, func(scanned int, hit *PortHit) {
		if hit != nil {
			found = hit
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("open port not reported")
	}
	if found.Port != openPort || !strings.Contains(found.Banner, "TestServer") || !strings.Contains(found.Banner, "200") {
		t.Fatalf("hit = %+v", found)
	}
}

func TestScanPortsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanner := NewPortScanner(config.PortScanConfig{Concurrent: 1, Timeout: 200 * time.Millisecond, BannerTimeout: 200 * time.Millisecond})
	err := scanner.ScanPorts(ctx, "127.0.0.1", "127.0.0.1", []PortService{{Port: 1, Service: "X", Group: GroupNetwork, Order: 1}}, nil)
	if err == nil {
		t.Fatal("expected cancel")
	}
}
