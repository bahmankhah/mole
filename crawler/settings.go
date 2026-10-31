package crawler

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/netx"
)

// RunSettings is the effective configuration of one job: global defaults
// merged with the job's overrides and limits.
type RunSettings struct {
	config.CrawlerConfig

	Headers          []models.HTTPHeader
	ProxyPool        string
	Proxies          []*url.URL
	StickyProxy      bool
	Fingerprint      string
	HostRateLimit    float64
	HeadlessFallback bool
	Actions          []models.BrowserAction
	Screenshot       bool
	FullPage         bool
	BlockResources   []string
	Formats          map[string]bool
	Detectors        map[string]bool // the formats that are detectors (product, article, …)
	OnlyMainContent  bool
	ExtractFields    []models.ExtractField
	ExtractURLMatch  *regexp.Regexp
	IncludeDocuments bool
	LLMPrompt        string
	LLMSchema        string
	Phrases          []string
	ChangeDetection  bool
	CompareWithJobID string
	TagRules         []TagRule
	KeepTags         map[string]bool // nil = keep every page
	MaxKeptPages     int
	// ScopeDomains are further base domains the crawl may follow links
	// into, besides the job's own; nil = the job's domain only.
	ScopeDomains map[string]bool
	// MaxPagesPerDomain caps the pages taken from any one site, so one
	// site of a multi-site crawl cannot spend the whole page budget.
	// 0 = no per-site cap.
	MaxPagesPerDomain int
	Limits            models.JobLimits
}

// TagRule is a compiled models.TagRule.
type TagRule struct {
	Pattern *regexp.Regexp
	Tag     string
}

// maxTagRules bounds the rules of a job; each is matched against every URL.
const maxTagRules = 100

var validBlockResources = map[string]bool{"image": true, "font": true, "media": true, "stylesheet": true}

var validActions = map[string]bool{"wait": true, "wait_for_selector": true, "click": true, "scroll": true, "type": true, "press": true, "evaluate": true}

// BuildRunSettings merges job settings and limits over the global defaults and
// validates them. It is used both when a job starts and when a job is created
// or edited, so invalid settings are rejected up front.
func BuildRunSettings(cfg *config.Config, s *models.JobSettings, limits *models.JobLimits, proxies *netx.ProxyManager) (*RunSettings, error) {
	rs := &RunSettings{
		CrawlerConfig: cfg.Crawler,
		Fingerprint:   cfg.Network.TLSFingerprint,
		Formats:       map[string]bool{},
	}
	if rs.HeadlessRenderWait <= 0 {
		rs.HeadlessRenderWait = 5
	}
	if limits != nil {
		rs.Limits = *limits
	}
	if s != nil {
		if err := mergeSettings(rs, s, proxies); err != nil {
			return nil, err
		}
	}
	// The stricter of settings.max_pages and limits.max_pages wins.
	if rs.Limits.MaxPages > 0 && (rs.MaxPages == 0 || rs.Limits.MaxPages < rs.MaxPages) {
		rs.MaxPages = rs.Limits.MaxPages
	}
	if rs.MaxConcurrentRequests < 1 {
		rs.MaxConcurrentRequests = 1
	}
	if rs.MaxConcurrentRequests > 64 {
		rs.MaxConcurrentRequests = 64
	}
	if rs.RequestTimeout <= 0 {
		rs.RequestTimeout = 30 * time.Second
	}
	if rs.IncludeDocuments {
		rs.SkipExtensions = withoutExtensions(rs.SkipExtensions, content.DocumentExtensions)
	}
	if !netx.ValidFingerprint(rs.Fingerprint) {
		rs.Fingerprint = netx.FingerprintChrome
	}
	return rs, nil
}

func mergeSettings(rs *RunSettings, s *models.JobSettings, proxies *netx.ProxyManager) error {
	if s.MaxConcurrentRequests != nil {
		rs.MaxConcurrentRequests = *s.MaxConcurrentRequests
	}
	if s.RequestTimeoutSec != nil {
		rs.RequestTimeout = time.Duration(*s.RequestTimeoutSec) * time.Second
	}
	if s.PolitenessDelayMs != nil {
		rs.PolitenessDelay = time.Duration(*s.PolitenessDelayMs) * time.Millisecond
	}
	if s.MaxDepth != nil {
		rs.MaxDepth = *s.MaxDepth
	}
	if s.MaxPages != nil {
		rs.MaxPages = *s.MaxPages
	}
	if s.MaxPagesPerDomain != nil {
		rs.MaxPagesPerDomain = *s.MaxPagesPerDomain
	}
	if s.UserAgent != nil {
		rs.UserAgent = *s.UserAgent
	}
	if s.MaxRetries != nil {
		rs.MaxRetries = *s.MaxRetries
	}
	if s.RespectRobotsTxt != nil {
		rs.RespectRobotsTxt = *s.RespectRobotsTxt
	}
	if len(s.SkipExtensions) > 0 {
		rs.SkipExtensions = s.SkipExtensions
	}
	if s.SkipContentDuplicates != nil {
		rs.SkipContentDuplicates = *s.SkipContentDuplicates
	}
	if s.UseHeadlessBrowser != nil {
		rs.UseHeadlessBrowser = *s.UseHeadlessBrowser
	}
	if s.HeadlessWaitSelector != nil {
		rs.HeadlessWaitSelector = *s.HeadlessWaitSelector
	}
	if s.HeadlessRenderWait != nil && *s.HeadlessRenderWait > 0 {
		rs.HeadlessRenderWait = *s.HeadlessRenderWait
	}
	if s.EnableSemanticSearch != nil {
		rs.EnableSemanticSearch = *s.EnableSemanticSearch
	}
	if s.EnableWordExtraction != nil {
		rs.EnableWordExtraction = *s.EnableWordExtraction
	}
	if s.SaveTextContent != nil {
		rs.SaveTextContent = *s.SaveTextContent
	}
	if s.AfterCrawlPlugin != nil {
		rs.AfterCrawlPlugin = strings.TrimSpace(*s.AfterCrawlPlugin)
	}
	if s.AfterJobPlugin != nil {
		rs.AfterJobPlugin = strings.TrimSpace(*s.AfterJobPlugin)
	}
	if s.EnableStemming != nil {
		rs.EnableStemming = *s.EnableStemming
	}
	if s.EnableLemmatization != nil {
		rs.EnableLemmatization = *s.EnableLemmatization
	}
	if s.DefaultLanguage != nil {
		rs.DefaultLanguage = *s.DefaultLanguage
	}
	if s.UseCrawlPhrasesOnly != nil {
		rs.UseCrawlPhrasesOnly = *s.UseCrawlPhrasesOnly
	}
	if len(s.RequestHeaders) > 0 {
		rs.Headers = append([]models.HTTPHeader(nil), s.RequestHeaders...)
	}

	for _, p := range s.URLIncludePatterns {
		if _, err := regexp.Compile("(?i)" + p); err != nil {
			return fmt.Errorf("invalid include pattern %q: %v", p, err)
		}
	}
	for _, p := range s.URLExcludePatterns {
		if _, err := regexp.Compile("(?i)" + p); err != nil {
			return fmt.Errorf("invalid exclude pattern %q: %v", p, err)
		}
	}

	if s.ProxyPool != nil {
		pool := strings.TrimSpace(*s.ProxyPool)
		if !proxies.HasPool(pool) {
			return fmt.Errorf("unknown proxy pool %q", pool)
		}
		rs.ProxyPool = pool
	}
	if len(s.Proxies) > 0 {
		list, err := netx.ParseProxyList(s.Proxies)
		if err != nil {
			return err
		}
		rs.Proxies = list
	}
	if s.StickyProxy != nil {
		rs.StickyProxy = *s.StickyProxy
	}
	if s.TLSFingerprint != nil && *s.TLSFingerprint != "" {
		if !netx.ValidFingerprint(*s.TLSFingerprint) {
			return fmt.Errorf("unknown TLS fingerprint %q (use one of %s)", *s.TLSFingerprint, strings.Join(netx.Fingerprints, ", "))
		}
		rs.Fingerprint = *s.TLSFingerprint
	}
	if s.HostRateLimit != nil {
		if *s.HostRateLimit < 0 {
			return fmt.Errorf("host_rate_limit must be >= 0")
		}
		rs.HostRateLimit = *s.HostRateLimit
	}
	if s.HeadlessFallback != nil {
		rs.HeadlessFallback = *s.HeadlessFallback
	}
	if len(s.BrowserActions) > 20 {
		return fmt.Errorf("at most 20 browser actions are allowed")
	}
	for i, a := range s.BrowserActions {
		if !validActions[a.Type] {
			return fmt.Errorf("browser action %d: unknown type %q", i+1, a.Type)
		}
		if (a.Type == "click" || a.Type == "type" || a.Type == "wait_for_selector") && strings.TrimSpace(a.Selector) == "" {
			return fmt.Errorf("browser action %d (%s): selector is required", i+1, a.Type)
		}
		if a.Type == "evaluate" && strings.TrimSpace(a.Script) == "" && strings.TrimSpace(a.Value) == "" {
			return fmt.Errorf("browser action %d (evaluate): script is required", i+1)
		}
	}
	rs.Actions = s.BrowserActions
	if s.Screenshot != nil {
		rs.Screenshot = *s.Screenshot
	}
	if s.FullPageScreenshot != nil {
		rs.FullPage = *s.FullPageScreenshot
	}
	for _, r := range s.BlockResources {
		if !validBlockResources[r] {
			return fmt.Errorf("unknown resource type to block %q", r)
		}
	}
	rs.BlockResources = s.BlockResources
	for _, f := range s.Formats {
		ok := false
		for _, known := range models.AllFormats {
			if f == known {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("unknown format %q (use %s)", f, strings.Join(models.AllFormats, ", "))
		}
		rs.Formats[f] = true
	}
	rs.Detectors = content.DetectFormats(rs.Formats)
	if s.OnlyMainContent != nil {
		rs.OnlyMainContent = *s.OnlyMainContent
	}
	if len(s.ExtractFields) > 0 {
		if err := content.ValidateFields(s.ExtractFields); err != nil {
			return err
		}
		rs.ExtractFields = s.ExtractFields
	}
	if s.ExtractURLMatch != nil && strings.TrimSpace(*s.ExtractURLMatch) != "" {
		re, err := regexp.Compile(*s.ExtractURLMatch)
		if err != nil {
			return fmt.Errorf("invalid extract_url_match: %v", err)
		}
		rs.ExtractURLMatch = re
	}
	if s.IncludeDocuments != nil {
		rs.IncludeDocuments = *s.IncludeDocuments
	}
	if s.LLMPrompt != nil {
		rs.LLMPrompt = strings.TrimSpace(*s.LLMPrompt)
	}
	if s.LLMSchema != nil {
		rs.LLMSchema = strings.TrimSpace(*s.LLMSchema)
	}
	for _, p := range s.Phrases {
		if p = strings.TrimSpace(p); p != "" {
			rs.Phrases = append(rs.Phrases, p)
		}
	}
	if s.ChangeDetection != nil {
		rs.ChangeDetection = *s.ChangeDetection
	}
	if s.CompareWithJobID != nil {
		rs.CompareWithJobID = strings.TrimSpace(*s.CompareWithJobID)
	}
	if len(s.ScopeDomains) > 0 {
		rs.ScopeDomains = map[string]bool{}
		for _, d := range s.ScopeDomains {
			domain := modules.CleanDomain(strings.TrimSpace(d))
			if domain == "" {
				return fmt.Errorf("invalid scope domain %q", d)
			}
			rs.ScopeDomains[domain] = true
		}
	}
	rules, err := CompileTagRules(s.TagRules)
	if err != nil {
		return err
	}
	rs.TagRules = rules
	if len(s.KeepTags) > 0 {
		rs.KeepTags = map[string]bool{}
		for _, t := range s.KeepTags {
			if strings.TrimSpace(t) == models.KeepTagAuto {
				added := false
				for format := range rs.Detectors {
					for pageType, f := range models.PageTypeDetector {
						if f == format {
							rs.KeepTags[pageType] = true
							added = true
						}
					}
				}
				if !added {
					return fmt.Errorf(`keep_tags "auto" keeps the types of the enabled detectors: enable product, article, job_posting or event`)
				}
				continue
			}
			tag := models.NormalizeTag(t)
			if tag == "" {
				return fmt.Errorf("invalid keep tag %q", t)
			}
			rs.KeepTags[tag] = true
		}
	}
	if s.MaxKeptPages != nil {
		if *s.MaxKeptPages < 0 {
			return fmt.Errorf("max_kept_pages must be >= 0")
		}
		rs.MaxKeptPages = *s.MaxKeptPages
	}
	return nil
}

// CompileTagRules validates and compiles tag rules (case-insensitive regexes).
func CompileTagRules(rules []models.TagRule) ([]TagRule, error) {
	if len(rules) > maxTagRules {
		return nil, fmt.Errorf("at most %d tag rules are allowed", maxTagRules)
	}
	out := make([]TagRule, 0, len(rules))
	for i, r := range rules {
		tag := models.NormalizeTag(r.Tag)
		if tag == "" {
			return nil, fmt.Errorf("tag rule %d: invalid tag %q (1-%d characters, no commas or quotes)", i+1, r.Tag, models.MaxTagLength)
		}
		if strings.TrimSpace(r.Pattern) == "" {
			return nil, fmt.Errorf("tag rule %d (%s): pattern is required", i+1, tag)
		}
		re, err := regexp.Compile("(?i)" + strings.TrimSpace(r.Pattern))
		if err != nil {
			return nil, fmt.Errorf("tag rule %d (%s): invalid pattern: %v", i+1, tag, err)
		}
		out = append(out, TagRule{Pattern: re, Tag: tag})
	}
	return out, nil
}

// pageTypeTags are the tags that are also page types.
var pageTypeTags = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range models.AllPageTypes {
		m[t] = true
	}
	return m
}()

// TagPage returns a page's effective type and tags: the first matching rule
// whose tag is a page type overrides the detected type; the tags are the
// type (unless "other" or unknown) plus every matching rule's tag. Both
// rawURL and finalURL (after redirects, may be "") are matched.
func TagPage(rules []TagRule, rawURL, finalURL, detected string) (pageType string, tags []string) {
	pageType = detected
	overridden := false
	seen := map[string]bool{}
	var ruleTags []string
	for _, r := range rules {
		if !r.Pattern.MatchString(rawURL) && (finalURL == "" || !r.Pattern.MatchString(finalURL)) {
			continue
		}
		if pageTypeTags[r.Tag] && !overridden {
			pageType, overridden = r.Tag, true
		}
		if !seen[r.Tag] {
			seen[r.Tag] = true
			ruleTags = append(ruleTags, r.Tag)
		}
	}
	if pageType != "" && pageType != models.PageTypeOther && !seen[pageType] {
		tags = append(tags, pageType)
	}
	return pageType, append(tags, ruleTags...)
}

// Keeps reports whether a page with these tags is saved in full.
func (rs *RunSettings) Keeps(tags []string) bool {
	if rs.KeepTags == nil {
		return true
	}
	for _, t := range tags {
		if rs.KeepTags[t] {
			return true
		}
	}
	return false
}

// withoutExtensions returns list without the given extensions, as a new slice
// (list may be shared with the global configuration).
func withoutExtensions(list, remove []string) []string {
	drop := make(map[string]bool, len(remove))
	for _, e := range remove {
		drop[strings.ToLower(e)] = true
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		norm := strings.ToLower(strings.TrimSpace(e))
		if !strings.HasPrefix(norm, ".") {
			norm = "." + norm
		}
		if !drop[norm] {
			out = append(out, e)
		}
	}
	return out
}

// convertsDocuments reports whether PDF/DOCX pages are worth converting: an
// output format, LLM extraction or change detection uses their text. (CSS,
// XPath and JSON-LD extraction do not apply to documents.)
func (rs *RunSettings) convertsDocuments() bool {
	f := rs.Formats
	return f[models.FormatMarkdown] || f[models.FormatText] || f[models.FormatMetadata] || rs.LLMPrompt != "" || rs.ChangeDetection
}

// NeedsContent reports whether pages must be analyzed into PageContent rows.
func (rs *RunSettings) NeedsContent() bool {
	return len(rs.Formats) > 0 || len(rs.ExtractFields) > 0 || rs.LLMPrompt != "" || rs.Screenshot || rs.ChangeDetection
}

// ExtractsURL reports whether structured/LLM extraction applies to rawURL.
func (rs *RunSettings) ExtractsURL(rawURL string) bool {
	return rs.ExtractURLMatch == nil || rs.ExtractURLMatch.MatchString(rawURL)
}
