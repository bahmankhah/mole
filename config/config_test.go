package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyLegacyScriptFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte(`
crawler:
  after_crawl_script: true
  after_job_script: true
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(path)
	if cfg.Crawler.AfterCrawlPlugin != "divar" || cfg.Crawler.AfterJobPlugin != "divar" {
		t.Fatalf("plugins=%q/%q", cfg.Crawler.AfterCrawlPlugin, cfg.Crawler.AfterJobPlugin)
	}
}

func TestPluginIDWinsOverLegacyYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte(`
crawler:
  after_crawl_plugin: custom
  after_crawl_script: true
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(path)
	if cfg.Crawler.AfterCrawlPlugin != "custom" {
		t.Fatalf("got %q", cfg.Crawler.AfterCrawlPlugin)
	}
}
