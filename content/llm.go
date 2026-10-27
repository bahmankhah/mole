package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/resolver/crawler/config"
)

// ErrLLMDisabled is returned when no LLM endpoint is configured.
var ErrLLMDisabled = errors.New("LLM extraction is not configured (set llm.base_url and llm.model)")

// LLMClient calls an OpenAI-compatible chat completions endpoint.
type LLMClient struct {
	baseURL  string
	apiKey   string
	model    string
	maxChars int
	http     *http.Client
}

// NewLLMClient returns nil when the endpoint is not configured.
func NewLLMClient(cfg config.LLMConfig) *LLMClient {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	maxChars := cfg.MaxInputChars
	if maxChars <= 0 {
		maxChars = 24000
	}
	return &LLMClient{
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		maxChars: maxChars,
		http:     &http.Client{Timeout: timeout},
	}
}

// Model returns the configured model name.
func (c *LLMClient) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Extract asks the model to pull structured data out of page content. schema
// is an optional JSON schema (or example object) describing the expected shape.
// It returns the JSON produced by the model and the tokens used.
func (c *LLMClient) Extract(ctx context.Context, prompt, schema, pageURL, pageContent string) (json.RawMessage, int, error) {
	if c == nil {
		return nil, 0, ErrLLMDisabled
	}
	if r := []rune(pageContent); len(r) > c.maxChars {
		pageContent = string(r[:c.maxChars])
	}
	system := "You extract structured data from web pages. Reply with one JSON value only: no prose, no Markdown code fences. " +
		"Use null for values that are not on the page. Keep text in its original language (often Persian)."
	if strings.TrimSpace(schema) != "" {
		system += "\nThe JSON must match this schema:\n" + schema
	}
	user := strings.TrimSpace(prompt)
	if user == "" {
		user = "Extract the main structured information from this page."
	}
	user += "\n\nPage URL: " + pageURL + "\n\nPage content:\n" + pageContent

	reqBody, _ := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("llm response: %w", err)
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, 0, fmt.Errorf("llm response (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if cr.Error != nil {
		return nil, cr.Usage.TotalTokens, fmt.Errorf("llm error: %s", cr.Error.Message)
	}
	if resp.StatusCode >= 300 || len(cr.Choices) == 0 {
		return nil, cr.Usage.TotalTokens, fmt.Errorf("llm returned HTTP %d with no choices", resp.StatusCode)
	}
	out, err := ParseJSONReply(cr.Choices[0].Message.Content)
	return out, cr.Usage.TotalTokens, err
}

// ParseJSONReply extracts a JSON value from a model reply that may be wrapped
// in code fences or surrounded by prose.
func ParseJSONReply(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		i, j := strings.Index(s, pair[0]), strings.LastIndex(s, pair[1])
		if i >= 0 && j > i && json.Valid([]byte(s[i:j+1])) {
			return json.RawMessage(s[i : j+1]), nil
		}
	}
	return nil, fmt.Errorf("model did not return valid JSON: %s", truncate(s, 200))
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
