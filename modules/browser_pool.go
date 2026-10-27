package modules

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/models"
)

// ErrBrowserPoolClosed is returned by Fetch after Close.
var ErrBrowserPoolClosed = errors.New("browser pool closed")

// BrowserPool keeps long-lived `headless_fetch.py --serve` processes, each
// holding one Chromium instance that renders one page at a time. It replaces
// spawning a Python process and a browser for every page.
type BrowserPool struct {
	python       string
	script       string
	blockPrivate bool
	slots        chan *browserProc // nil entries are slots without a running process
	closed       atomic.Bool
	seq          atomic.Uint64
	mu           sync.Mutex
	procs        map[*browserProc]struct{}
}

type browserProc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan []byte
	done     chan struct{} // closed when the process has exited
	quit     chan struct{} // closed when the pool gives up on the process
	quitOnce sync.Once
}

// NewBrowserPool creates a pool of size processes; they start lazily.
// blockPrivate makes the browser refuse private-network requests (SSRF guard).
func NewBrowserPool(pythonCmd, scriptPath string, size int, blockPrivate bool) *BrowserPool {
	if size < 1 {
		size = 1
	}
	p := &BrowserPool{
		python:       pythonCmd,
		script:       scriptPath,
		blockPrivate: blockPrivate,
		slots:        make(chan *browserProc, size),
		procs:        map[*browserProc]struct{}{},
	}
	for i := 0; i < size; i++ {
		p.slots <- nil
	}
	return p
}

// Size returns the number of browser slots.
func (p *BrowserPool) Size() int { return cap(p.slots) }

type browserRequest struct {
	ID                   string                 `json:"id"`
	URL                  string                 `json:"url"`
	Timeout              float64                `json:"timeout"`
	RenderWait           float64                `json:"render_wait"`
	WaitSelector         string                 `json:"wait_selector,omitempty"`
	UserAgent            string                 `json:"user_agent,omitempty"`
	Headers              map[string]string      `json:"headers,omitempty"`
	Proxy                map[string]string      `json:"proxy,omitempty"`
	CookieDir            string                 `json:"cookie_dir,omitempty"`
	Screenshot           bool                   `json:"screenshot,omitempty"`
	FullPage             bool                   `json:"full_page,omitempty"`
	BlockResources       []string               `json:"block_resources,omitempty"`
	BlockPrivateNetworks bool                   `json:"block_private_networks"`
	Actions              []models.BrowserAction `json:"actions,omitempty"`
}

type browserResponse struct {
	ID            *string         `json:"id"`
	Type          string          `json:"type"`
	URL           string          `json:"url"`
	StatusCode    int             `json:"status_code"`
	ContentType   string          `json:"content_type"`
	Body          string          `json:"body"`
	Error         string          `json:"error"`
	Screenshot    string          `json:"screenshot"`
	ActionResults json.RawMessage `json:"action_results"`
}

// Fetch renders rawURL in a pooled browser.
func (p *BrowserPool) Fetch(ctx context.Context, rawURL string, opts FetchOptions) *FetchResult {
	start := time.Now()
	res := p.fetch(ctx, rawURL, opts)
	res.FetchedWith = "browser"
	res.Duration = time.Since(start)
	res.ViaProxy = opts.Proxy != nil
	return res
}

func (p *BrowserPool) fetch(ctx context.Context, rawURL string, opts FetchOptions) *FetchResult {
	if p.closed.Load() {
		return &FetchResult{URL: rawURL, Error: ErrBrowserPoolClosed}
	}
	var proc *browserProc
	select {
	case proc = <-p.slots:
	case <-ctx.Done():
		return &FetchResult{URL: rawURL, Error: ctx.Err()}
	}
	release := func(keep *browserProc) {
		if p.closed.Load() && keep != nil {
			p.kill(keep)
			keep = nil
		}
		p.slots <- keep
	}

	if proc == nil || proc.exited() {
		var err error
		proc, err = p.start(ctx)
		if err != nil {
			release(nil)
			return &FetchResult{URL: rawURL, Error: fmt.Errorf("start browser: %w", err)}
		}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	renderWait := opts.RenderWait
	if renderWait <= 0 {
		renderWait = 5
	}
	req := browserRequest{
		ID:                   strconv.FormatUint(p.seq.Add(1), 10),
		URL:                  rawURL,
		Timeout:              timeout.Seconds(),
		RenderWait:           float64(renderWait),
		WaitSelector:         opts.WaitSelector,
		CookieDir:            opts.CookieDir,
		Screenshot:           opts.Screenshot,
		FullPage:             opts.FullPage,
		BlockResources:       opts.BlockResources,
		BlockPrivateNetworks: p.blockPrivate,
		Actions:              opts.Actions,
	}
	// Bot-like UAs defeat the browser's stealth measures; let the script use its default.
	if opts.UserAgent != "" && looksLikeBrowserUA(opts.UserAgent) {
		req.UserAgent = opts.UserAgent
	}
	if len(opts.Headers) > 0 {
		req.Headers = map[string]string{}
		for _, h := range opts.Headers {
			if h.Name != "" {
				req.Headers[h.Name] = h.Value
			}
		}
	}
	if opts.Proxy != nil {
		pr := map[string]string{"server": opts.Proxy.Scheme + "://" + opts.Proxy.Host}
		if opts.Proxy.User != nil {
			pr["username"] = opts.Proxy.User.Username()
			pr["password"], _ = opts.Proxy.User.Password()
		}
		req.Proxy = pr
	}
	line, _ := json.Marshal(req)
	line = append(line, '\n')
	if _, err := proc.stdin.Write(line); err != nil {
		p.kill(proc)
		release(nil)
		return &FetchResult{URL: rawURL, Error: fmt.Errorf("browser write: %w", err)}
	}

	// Budget for navigation, CDN retries, SPA waits, actions and screenshots.
	budget := 3*timeout + time.Duration(renderWait)*4*time.Second + 60*time.Second
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// The process is mid-request; its next reply would be out of sync.
			p.kill(proc)
			release(nil)
			return &FetchResult{URL: rawURL, Error: ctx.Err()}
		case <-timer.C:
			p.kill(proc)
			release(nil)
			return &FetchResult{URL: rawURL, Error: fmt.Errorf("browser timed out after %s", budget)}
		case <-proc.done:
			release(nil)
			return &FetchResult{URL: rawURL, Error: errors.New("browser process exited")}
		case raw := <-proc.lines:
			var resp browserResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				p.kill(proc)
				release(nil)
				return &FetchResult{URL: rawURL, Error: fmt.Errorf("browser reply: %w", err)}
			}
			if resp.ID == nil || *resp.ID != req.ID {
				continue // stale line from an earlier request
			}
			release(proc)
			out := &FetchResult{
				URL:           resp.URL,
				StatusCode:    resp.StatusCode,
				ContentType:   resp.ContentType,
				Body:          []byte(resp.Body),
				ActionResults: resp.ActionResults,
				Header:        http.Header{},
			}
			if out.URL == "" {
				out.URL = rawURL
			}
			if resp.Screenshot != "" {
				if png, err := base64.StdEncoding.DecodeString(resp.Screenshot); err == nil {
					out.Screenshot = png
				}
			}
			if resp.Error != "" {
				out.Error = fmt.Errorf("headless: %s", resp.Error)
			}
			return out
		}
	}
}

// start launches a serve-mode process and waits for its ready line.
func (p *BrowserPool) start(ctx context.Context) (*browserProc, error) {
	cmd := exec.Command(p.python, p.script, "--serve")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc := &browserProc{cmd: cmd, stdin: stdin, lines: make(chan []byte, 4), done: make(chan struct{}), quit: make(chan struct{})}
	go func() {
		r := bufio.NewReaderSize(stdout, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case proc.lines <- line:
				case <-proc.quit: // nobody is listening any more; keep draining to EOF
				}
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
		close(proc.done)
	}()
	p.mu.Lock()
	p.procs[proc] = struct{}{}
	p.mu.Unlock()

	wait := time.NewTimer(90 * time.Second)
	defer wait.Stop()
	for {
		select {
		case line := <-proc.lines:
			var msg browserResponse
			if json.Unmarshal(line, &msg) == nil && msg.Type == "ready" {
				log.Printf("[BrowserPool] Browser process %d ready", cmd.Process.Pid)
				return proc, nil
			}
		case <-proc.done:
			p.forget(proc)
			return nil, errors.New("browser process exited during startup (is playwright installed? run plugins/setup_python.sh)")
		case <-wait.C:
			p.kill(proc)
			return nil, errors.New("browser process did not become ready within 90s")
		case <-ctx.Done():
			p.kill(proc)
			return nil, ctx.Err()
		}
	}
}

func (b *browserProc) exited() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

func (p *BrowserPool) kill(proc *browserProc) {
	if proc == nil {
		return
	}
	proc.quitOnce.Do(func() { close(proc.quit) })
	_ = proc.stdin.Close()
	if proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
	}
	p.forget(proc)
}

func (p *BrowserPool) forget(proc *browserProc) {
	p.mu.Lock()
	delete(p.procs, proc)
	p.mu.Unlock()
}

// Close stops every browser process. Requests in flight fail.
func (p *BrowserPool) Close() {
	if p.closed.Swap(true) {
		return
	}
	p.mu.Lock()
	procs := make([]*browserProc, 0, len(p.procs))
	for proc := range p.procs {
		procs = append(procs, proc)
	}
	p.mu.Unlock()
	for _, proc := range procs {
		// Closing stdin lets the script exit cleanly; kill as a fallback.
		proc.quitOnce.Do(func() { close(proc.quit) })
		_ = proc.stdin.Close()
		select {
		case <-proc.done:
		case <-time.After(5 * time.Second):
			p.kill(proc)
		}
		p.forget(proc)
	}
}
