package models

import (
	"encoding/json"
	"testing"
)

func TestJobSettingsLegacyScriptBools(t *testing.T) {
	var s JobSettings
	if err := json.Unmarshal([]byte(`{"after_crawl_script":true,"after_job_script":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.AfterCrawlPlugin == nil || *s.AfterCrawlPlugin != "divar" {
		t.Fatalf("after_crawl_plugin=%v", s.AfterCrawlPlugin)
	}
	if s.AfterJobPlugin == nil || *s.AfterJobPlugin != "divar" {
		t.Fatalf("after_job_plugin=%v", s.AfterJobPlugin)
	}
}

func TestJobSettingsLegacyScriptFalse(t *testing.T) {
	var s JobSettings
	if err := json.Unmarshal([]byte(`{"after_crawl_script":false,"after_job_script":false}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.AfterCrawlPlugin == nil || *s.AfterCrawlPlugin != "" {
		t.Fatalf("after_crawl_plugin=%v", s.AfterCrawlPlugin)
	}
	if s.AfterJobPlugin == nil || *s.AfterJobPlugin != "" {
		t.Fatalf("after_job_plugin=%v", s.AfterJobPlugin)
	}
}

func TestJobSettingsPluginIDWinsOverLegacyBool(t *testing.T) {
	var s JobSettings
	raw := `{"after_crawl_plugin":"other","after_crawl_script":true}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.AfterCrawlPlugin == nil || *s.AfterCrawlPlugin != "other" {
		t.Fatalf("after_crawl_plugin=%v", s.AfterCrawlPlugin)
	}
}

func TestSanitizePluginIDRejectsTraversal(t *testing.T) {
	bad := "../etc"
	s := JobSettings{AfterCrawlPlugin: &bad}
	s.SanitizeRequest()
	if s.AfterCrawlPlugin == nil || *s.AfterCrawlPlugin != "" {
		t.Fatalf("got %v", s.AfterCrawlPlugin)
	}
}

func TestJobStatusSettingsEditable(t *testing.T) {
	if !JobStatusPending.SettingsEditable() {
		t.Fatal("pending should be editable")
	}
	if !JobStatusPaused.SettingsEditable() {
		t.Fatal("paused should be editable")
	}
	for _, s := range []JobStatus{JobStatusRunning, JobStatusCompleted, JobStatusFailed, JobStatusCancelled} {
		if s.SettingsEditable() {
			t.Fatalf("%s should not be editable", s)
		}
	}
}
