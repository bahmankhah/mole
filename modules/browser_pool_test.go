package modules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/models"
)

// TestBrowserPool drives real Chromium processes. Run with MOLE_BROWSER_TESTS=1.
func TestBrowserPool(t *testing.T) {
	if os.Getenv("MOLE_BROWSER_TESTS") == "" {
		t.Skip("set MOLE_BROWSER_TESTS=1 to run headless browser tests")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>T</title></head><body><div id="out">` + strings.Repeat("x", 3000) +
			`</div><button id="b" onclick="document.getElementById('out').textContent+='CLICKED'">b</button>` +
			`<script>document.body.insertAdjacentHTML('beforeend','<p id=js>RENDERED</p>')</script></body></html>`))
	}))
	defer srv.Close()

	pool := NewBrowserPool(FindPython("../plugins/.venv/bin/python3"), "../plugins/headless_fetch.py", 2, false)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	res := pool.Fetch(ctx, srv.URL, FetchOptions{RenderWait: 1, Screenshot: true,
		Actions: []models.BrowserAction{{Type: "click", Selector: "#b"}}})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	body := string(res.Body)
	if !strings.Contains(body, "RENDERED") || !strings.Contains(body, "CLICKED") {
		t.Fatalf("JS or action not applied: %d bytes", len(body))
	}
	if len(res.Screenshot) < 100 || string(res.Screenshot[1:4]) != "PNG" {
		t.Fatal("screenshot missing")
	}
	// Second request reuses the warm browser.
	start := time.Now()
	if res := pool.Fetch(ctx, srv.URL, FetchOptions{RenderWait: 1}); res.Error != nil || res.StatusCode != 200 {
		t.Fatalf("second fetch: %v %d", res.Error, res.StatusCode)
	}
	t.Logf("warm fetch took %s", time.Since(start))

	blocked := NewBrowserPool(FindPython("../plugins/.venv/bin/python3"), "../plugins/headless_fetch.py", 1, true)
	defer blocked.Close()
	if res := blocked.Fetch(ctx, srv.URL, FetchOptions{RenderWait: 1}); res.Error == nil || !strings.Contains(res.Error.Error(), "private network") {
		t.Fatalf("private network not blocked: %v", res.Error)
	}
}
