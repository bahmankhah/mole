package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RejectedError is a 4xx answer from the control node (for example a revoked
// token). Unlike a network error it will not go away by retrying.
type RejectedError struct {
	Status  int
	Code    string
	Message string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("control node refused the request (%d %s): %s", e.Status, e.Code, e.Message)
}

// ValidateControlURL normalises the control node's base URL. Plain HTTP is
// refused unless allowHTTP is set or the host is a loopback or private
// address (for example over a VPN), because the answer carries the database
// password and other secrets.
func ValidateControlURL(raw string, allowHTTP bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("control URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("control URL must start with https:// or http://")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("control URL has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("control URL must not contain credentials, a query or a fragment")
	}
	if u.Scheme == "http" && !allowHTTP && !isPrivateHost(u.Hostname()) {
		return "", fmt.Errorf("refusing plain http to %s: the answer contains secrets. Use https, a private/VPN address, or --allow-http", u.Hostname())
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), nil
}

func isPrivateHost(host string) bool {
	if isLoopback(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

// cgnat is 100.64.0.0/10, used by VPNs such as Tailscale.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// FetchConfig asks the control node for this node's configuration.
func FetchConfig(ctx context.Context, controlURL, token string, req JoinRequest) (*JoinResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL+"/cluster/join", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("reach control node %s: %w", controlURL, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read answer of control node: %w", err)
	}
	var envelope struct {
		Data  *JoinResponse `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	jsonErr := json.Unmarshal(data, &envelope)
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		rej := &RejectedError{Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
		if jsonErr == nil && envelope.Error != nil {
			rej.Code, rej.Message = envelope.Error.Code, envelope.Error.Message
		}
		if res.StatusCode == http.StatusNotFound && rej.Code == "" {
			rej.Message = "no /cluster/join endpoint: is this the URL of a Mole server (\"mole serve\") of the same version?"
		}
		return nil, rej
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control node %s answered HTTP %d", controlURL, res.StatusCode)
	}
	if jsonErr != nil || envelope.Data == nil || envelope.Data.Config == "" {
		return nil, fmt.Errorf("control node %s sent an unexpected answer", controlURL)
	}
	return envelope.Data, nil
}

var nonIDChars = regexp.MustCompile(`[^a-z0-9-]+`)

// NewNodeID returns a worker id for a new node: its host name plus a random
// suffix, so cloned machines with the same host name still get distinct ids.
func NewNodeID(hostname string) string {
	base := strings.Trim(nonIDChars.ReplaceAllString(strings.ToLower(hostname), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "node"
	}
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	return base + "-" + hex.EncodeToString(buf)
}
