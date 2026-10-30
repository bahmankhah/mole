package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"strings"
	"time"

	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"gorm.io/gorm/clause"
)

// ScrapeRequest is a /v1/scrape request: a single-page scrape plus cache control.
type ScrapeRequest struct {
	crawler.ScrapeRequest
	// MaxAge accepts a cached result fetched at most this many seconds ago
	// (0 = always fetch).
	MaxAge int `json:"max_age,omitempty"`
	// StoreInCache saves a successful result for later max_age requests (default true).
	StoreInCache *bool `json:"store_in_cache,omitempty"`
}

// Scrape fetches one URL, or returns a cached result when req.MaxAge allows
// it. ownerRef scopes the cache for requests that may return private content.
func (m *Manager) Scrape(ctx context.Context, req ScrapeRequest, ownerRef string) (*crawler.ScrapeResult, error) {
	if req.MaxAge < 0 {
		return nil, invalidInput("max_age must not be negative")
	}
	cache := m.config.Cache.Enabled
	key, scope := "", ""
	if cache {
		var err error
		key, scope, err = scrapeCacheKey(req.ScrapeRequest, ownerRef)
		if err != nil {
			return nil, err
		}
	}
	if cache && req.MaxAge > 0 {
		if res := m.cachedScrape(key, time.Duration(req.MaxAge)*time.Second); res != nil {
			return res, nil
		}
	}
	res, err := m.engine.Scrape(ctx, req.ScrapeRequest)
	if err != nil {
		return nil, err
	}
	if cache && (req.StoreInCache == nil || *req.StoreInCache) && cacheable(res) {
		m.storeScrape(key, scope, res)
	}
	return res, nil
}

// scrapeCacheKey identifies a scrape by URL and settings. Requests that send
// headers, POST bodies, their own proxies or browser actions can return content
// only that customer may see, so their entries are scoped to the owner; all
// other entries are shared between owners.
func scrapeCacheKey(req crawler.ScrapeRequest, ownerRef string) (key, scope string, err error) {
	settings, err := json.Marshal(req.Settings)
	if err != nil {
		return "", "", err
	}
	if s := req.Settings; s != nil && (len(s.RequestHeaders) > 0 || s.RequestMethod != nil || s.RequestBody != nil ||
		len(s.Proxies) > 0 || len(s.BrowserActions) > 0) {
		scope = ownerRef
	}
	sum := sha256.Sum256([]byte("v1\n" + scope + "\n" + strings.TrimSpace(req.URL) + "\n" + string(settings)))
	return hex.EncodeToString(sum[:]), scope, nil
}

// cacheable reports whether a result is worth reusing: a successful fetch
// without conversion errors.
func cacheable(res *crawler.ScrapeResult) bool {
	return res.Error == "" && res.ExtractError == "" && res.StatusCode >= 200 && res.StatusCode < 300
}

func (m *Manager) cachedScrape(key string, maxAge time.Duration) *crawler.ScrapeResult {
	// Find, not First: a miss is normal and must not be logged as an error.
	var entries []models.ScrapeCacheEntry
	if err := m.db.Where("cache_key = ? AND created_at >= ?", key, time.Now().Add(-maxAge)).Limit(1).Find(&entries).Error; err != nil {
		log.Printf("[ScrapeCache] Lookup failed: %v", err)
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	entry := entries[0]
	zr, err := gzip.NewReader(bytes.NewReader(entry.Result))
	if err != nil {
		return nil
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	var res crawler.ScrapeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil
	}
	// The customer is not charged for work the engine did not do again.
	res.Usage = models.JobUsage{}
	res.DurationMs = 0
	res.Cached = true
	at := entry.CreatedAt
	res.CachedAt = &at
	return &res
}

func (m *Manager) storeScrape(key, scope string, res *crawler.ScrapeResult) {
	raw, err := json.Marshal(res)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return
	}
	if err := zw.Close(); err != nil {
		return
	}
	if limit := m.config.Cache.MaxEntryBytes; limit > 0 && buf.Len() > limit {
		return
	}
	url := res.URL
	if len(url) > 2048 {
		url = url[:2048]
	}
	entry := models.ScrapeCacheEntry{CacheKey: key, OwnerRef: scope, URL: url, Result: buf.Bytes(), CreatedAt: time.Now()}
	err = m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "cache_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"result", "created_at", "url", "owner_ref"}),
	}).Create(&entry).Error
	if err != nil {
		log.Printf("[ScrapeCache] Failed to store %s: %v", res.URL, err)
	}
}

// cleanupScrapeCache deletes entries older than cache.ttl.
func (m *Manager) cleanupScrapeCache() {
	ttl := m.config.Cache.TTL
	if ttl <= 0 {
		return
	}
	if err := m.db.Where("created_at < ?", time.Now().Add(-ttl)).Delete(&models.ScrapeCacheEntry{}).Error; err != nil {
		log.Printf("[ScrapeCache] Cleanup failed: %v", err)
	}
}
