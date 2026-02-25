package modules

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/config"
)

// Stemmer provides stemming and lemmatisation via a Python backend (Hazm for
// Persian, NLTK Snowball for English).  It caches results in-memory so the
// same token is never sent to Python twice in a single process lifetime.
type Stemmer struct {
	pythonCmd  string
	scriptPath string
	lang       string
	enabled    bool

	// In-memory cache: raw token → stemmed token.
	cache   map[string]string
	cacheMu sync.RWMutex
}

// NewStemmer creates a new stemmer backed by the Python stem_lemma.py script.
func NewStemmer(cfg config.CrawlerConfig) *Stemmer {
	scriptPath := "scripts/stem_lemma.py"
	if _, err := os.Stat(scriptPath); err != nil {
		// Try relative to executable
		candidates := []string{"./scripts/stem_lemma.py"}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				scriptPath = c
				break
			}
		}
	}

	lang := cfg.DefaultLanguage
	if lang == "" {
		lang = "fa"
	}

	enabled := cfg.EnableStemming

	pythonCmd := FindPython(cfg.PythonPath)

	return &Stemmer{
		pythonCmd:  pythonCmd,
		scriptPath: scriptPath,
		lang:       lang,
		enabled:    enabled,
		cache:      make(map[string]string, 4096),
	}
}

// Name returns the module name.
func (s *Stemmer) Name() string { return "stemmer" }

// Initialize validates the Python backend is reachable.
func (s *Stemmer) Initialize() error {
	if !s.enabled {
		log.Printf("[%s] Disabled by config", s.Name())
		return nil
	}

	// Quick ping to verify the script works.
	_, err := s.callPython(stemRequest{Command: "ping", Lang: s.lang})
	if err != nil {
		log.Printf("[%s] WARNING: Python backend unreachable: %v — stemming will be skipped", s.Name(), err)
		s.enabled = false
		return nil // non-fatal: we degrade gracefully
	}

	log.Printf("[%s] Initialized (lang=%s, python=%s)", s.Name(), s.lang, s.pythonCmd)
	return nil
}

// Shutdown gracefully stops the module.
func (s *Stemmer) Shutdown() error { return nil }

// Enabled reports whether stemming is active.
func (s *Stemmer) Enabled() bool { return s.enabled }

// StemTokens stems a list of already-tokenised words.  Cached results are
// returned immediately; only uncached tokens are sent to Python.
func (s *Stemmer) StemTokens(tokens []string) []string {
	if !s.enabled || len(tokens) == 0 {
		return tokens
	}

	result := make([]string, len(tokens))
	var uncached []string         // tokens that need Python
	uncachedIdx := make([]int, 0) // their positions in `result`

	s.cacheMu.RLock()
	for i, tok := range tokens {
		if v, ok := s.cache[tok]; ok {
			result[i] = v
		} else {
			uncached = append(uncached, tok)
			uncachedIdx = append(uncachedIdx, i)
		}
	}
	s.cacheMu.RUnlock()

	if len(uncached) == 0 {
		return result
	}

	// Call Python in batch mode.
	resp, err := s.callPython(stemRequest{
		Command: "batch",
		Texts:   uncached,
		Lang:    s.lang,
	})
	if err != nil {
		log.Printf("[Stemmer] batch call failed: %v — returning original tokens", err)
		for j, idx := range uncachedIdx {
			result[idx] = uncached[j]
		}
		return result
	}

	// Each input text is a single token, so each result list should have
	// exactly one element (or zero for very short tokens).
	s.cacheMu.Lock()
	for j, idx := range uncachedIdx {
		stemmed := uncached[j] // fallback
		if j < len(resp.Results) && len(resp.Results[j]) > 0 {
			stemmed = resp.Results[j][0]
		}
		result[idx] = stemmed
		s.cache[uncached[j]] = stemmed
	}
	s.cacheMu.Unlock()

	return result
}

// StemToken stems a single token (convenience wrapper).
func (s *Stemmer) StemToken(token string) string {
	if !s.enabled || token == "" {
		return token
	}

	s.cacheMu.RLock()
	if v, ok := s.cache[token]; ok {
		s.cacheMu.RUnlock()
		return v
	}
	s.cacheMu.RUnlock()

	res := s.StemTokens([]string{token})
	if len(res) > 0 {
		return res[0]
	}
	return token
}

// StemText takes free-form text, tokenises it in Python, and returns the
// list of stemmed tokens.
func (s *Stemmer) StemText(text string) []string {
	if !s.enabled || strings.TrimSpace(text) == "" {
		return nil
	}

	resp, err := s.callPython(stemRequest{
		Command: "process",
		Text:    text,
		Lang:    s.lang,
	})
	if err != nil {
		log.Printf("[Stemmer] process call failed: %v", err)
		return nil
	}

	// Populate cache while we have the data.
	s.cacheMu.Lock()
	for _, tok := range resp.Tokens {
		s.cache[tok] = tok // identity: already stemmed
	}
	s.cacheMu.Unlock()

	return resp.Tokens
}

// ── Python IPC types ────────────────────────────────────────────────────────

type stemRequest struct {
	Command string   `json:"command"`
	Text    string   `json:"text,omitempty"`
	Texts   []string `json:"texts,omitempty"`
	Lang    string   `json:"lang"`
}

type stemResponse struct {
	Tokens  []string   `json:"tokens,omitempty"`
	Results [][]string `json:"results,omitempty"`
	Error   string     `json:"error"`
}

func (s *Stemmer) callPython(req stemRequest) (*stemResponse, error) {
	input, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx := exec.Command(s.pythonCmd, s.scriptPath)
	ctx.Stdin = strings.NewReader(string(input))
	ctx.Stderr = os.Stderr

	// Timeout guard.
	done := make(chan struct{})
	var output []byte
	var cmdErr error
	go func() {
		output, cmdErr = ctx.Output()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		if ctx.Process != nil {
			ctx.Process.Kill()
		}
		return nil, fmt.Errorf("python script timed out after 30s")
	}

	if cmdErr != nil {
		return nil, fmt.Errorf("python exec: %w (output: %s)", cmdErr, string(output))
	}

	var resp stemResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (raw: %s)", err, string(output))
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("python error: %s", resp.Error)
	}

	return &resp, nil
}
