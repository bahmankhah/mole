package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// URLCleaner implements URL normalization and cleaning
type URLCleaner struct {
	// Tracking parameters to remove
	trackingParams []string
	// Fragment removal
	removeFragments bool
	// Force lowercase
	forceLowercase bool
	// Remove default ports
	removeDefaultPorts bool
	// Remove trailing slashes
	removeTrailingSlash bool
	// Sort query parameters
	sortQueryParams bool
}

// NewURLCleaner creates a new URL cleaner with default settings
func NewURLCleaner() *URLCleaner {
	return &URLCleaner{
		trackingParams: []string{
			"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
			"fbclid", "gclid", "gclsrc", "dclid", "msclkid", "zanpid", "igshid",
			"ref", "ref_src", "ref_url", "source", "src", "mc_cid", "mc_eid",
			"_ga", "_gl", "_hsenc", "_hsmi", "hsa_acc", "hsa_cam", "hsa_grp",
			"hsa_ad", "hsa_src", "hsa_tgt", "hsa_kw", "hsa_mt", "hsa_net",
			"hsa_ver", "trk", "trkCampaign", "li_fat_id", "s_kwcid",
			"redirect_log_mongo_id", "redirect_mongo_id", "sb_referer_host",
		},
		removeFragments:     true,
		forceLowercase:      true,
		removeDefaultPorts:  true,
		removeTrailingSlash: true,
		sortQueryParams:     true,
	}
}

// Name returns the module name
func (u *URLCleaner) Name() string {
	return "url_cleaner"
}

// Initialize sets up the module
func (u *URLCleaner) Initialize() error {
	log.Printf("[%s] Initialized with %d tracking parameters to remove", u.Name(), len(u.trackingParams))
	return nil
}

// Shutdown gracefully stops the module
func (u *URLCleaner) Shutdown() error {
	return nil
}

// ProcessURL normalizes and cleans a URL
func (u *URLCleaner) ProcessURL(rawURL string) (string, error) {
	// Trim whitespace
	rawURL = strings.TrimSpace(rawURL)

	if rawURL == "" {
		return "", fmt.Errorf("empty URL")
	}

	// Parse URL
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	// Must have scheme
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}

	// Only allow http and https
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}

	// Must have host
	if parsed.Host == "" {
		return "", fmt.Errorf("missing host")
	}

	// Lowercase scheme and host
	if u.forceLowercase {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
	}

	// Remove default ports
	if u.removeDefaultPorts {
		host := parsed.Hostname()
		port := parsed.Port()

		if (parsed.Scheme == "http" && port == "80") ||
			(parsed.Scheme == "https" && port == "443") {
			parsed.Host = host
		}
	}

	// Remove fragment
	if u.removeFragments {
		parsed.Fragment = ""
	}

	// Process query parameters
	query := parsed.Query()

	// Remove tracking parameters
	for _, param := range u.trackingParams {
		query.Del(param)
	}

	// Sort query parameters for consistency
	if u.sortQueryParams && len(query) > 0 {
		parsed.RawQuery = u.sortedQueryString(query)
	} else {
		parsed.RawQuery = query.Encode()
	}

	// Normalize path
	path := parsed.Path
	if path == "" {
		path = "/"
	}

	// Remove duplicate slashes
	path = regexp.MustCompile(`/+`).ReplaceAllString(path, "/")

	// Remove trailing slash (except for root)
	if u.removeTrailingSlash && len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}

	parsed.Path = path

	return parsed.String(), nil
}

// sortedQueryString returns query string with sorted parameters
func (u *URLCleaner) sortedQueryString(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		for _, value := range values[key] {
			parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}

	return strings.Join(parts, "&")
}

// HashURL creates a hash of a URL for deduplication
func (u *URLCleaner) HashURL(normalizedURL string) string {
	hash := sha256.Sum256([]byte(normalizedURL))
	return hex.EncodeToString(hash[:])
}

// ResolveURL resolves a relative URL against a base URL
func (u *URLCleaner) ResolveURL(baseURL, relativeURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}

	relative, err := url.Parse(relativeURL)
	if err != nil {
		return "", fmt.Errorf("invalid relative URL: %w", err)
	}

	resolved := base.ResolveReference(relative)
	return u.ProcessURL(resolved.String())
}

// IsSameDomain checks if two URLs belong to the same domain
func (u *URLCleaner) IsSameDomain(url1, url2 string) bool {
	domain1 := u.ExtractDomain(url1)
	domain2 := u.ExtractDomain(url2)
	return domain1 == domain2
}

// ExtractDomain extracts the domain from a URL
func (u *URLCleaner) ExtractDomain(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// ExtractBaseDomain extracts the base domain (without subdomain) from a URL
func (u *URLCleaner) ExtractBaseDomain(rawURL string) string {
	domain := u.ExtractDomain(rawURL)
	if domain == "" {
		return ""
	}

	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return domain
	}

	// Handle common TLDs like .co.uk, .com.br, etc.
	commonSecondLevel := map[string]bool{
		"co": true, "com": true, "net": true, "org": true,
		"edu": true, "gov": true, "ac": true,
	}

	if len(parts) >= 3 && commonSecondLevel[parts[len(parts)-2]] {
		return strings.Join(parts[len(parts)-3:], ".")
	}

	return strings.Join(parts[len(parts)-2:], ".")
}
