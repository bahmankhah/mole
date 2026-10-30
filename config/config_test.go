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

func TestMissingFileStillAppliesEnv(t *testing.T) {
	t.Setenv("MOLE_CONTROL_URL", "https://mole.example.com")
	t.Setenv("MOLE_JOIN_TOKEN", "mjt_x")
	t.Setenv("MOLE_WORKER_BROWSER", "false")
	cfg := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if !cfg.Cluster.Joined() {
		t.Errorf("cluster = %+v, want joined from environment", cfg.Cluster)
	}
	if cfg.Worker.Browser == nil || *cfg.Worker.Browser {
		t.Errorf("worker.browser = %v, want false", cfg.Worker.Browser)
	}
}

func TestNodeSettingsAndEnvPrecedence(t *testing.T) {
	t.Setenv("MOLE_WORKER_ID", "")
	t.Setenv("MOLE_WORKER_REGION", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
worker: {id: from-worker, region: ir, max_running_jobs: 2}
headless: {pool_size: 1}
cluster:
  node: {id: from-node, region: intl, max_running_jobs: 7, browser_pool_size: 4, browser: true}
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(path)
	if cfg.Worker.ID != "from-node" || cfg.Worker.Region != "intl" || cfg.Worker.MaxRunningJobs != 7 || cfg.Headless.PoolSize != 4 {
		t.Errorf("node settings not applied: %+v pool=%d", cfg.Worker, cfg.Headless.PoolSize)
	}
	if cfg.Worker.Browser == nil || !*cfg.Worker.Browser {
		t.Errorf("worker.browser = %v, want true", cfg.Worker.Browser)
	}
	t.Setenv("MOLE_WORKER_ID", "from-env")
	if cfg = LoadConfig(path); cfg.Worker.ID != "from-env" {
		t.Errorf("worker id = %q, want the environment to win", cfg.Worker.ID)
	}
}
