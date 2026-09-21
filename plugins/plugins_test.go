package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, root, id, meta, afterCrawl, afterJob string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if afterCrawl != "" {
		if err := os.WriteFile(filepath.Join(dir, afterCrawl), []byte("print('crawl')\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if afterJob != "" {
		if err := os.WriteFile(filepath.Join(dir, afterJob), []byte("print('job')\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidID(t *testing.T) {
	ok := []string{"divar", "a", "Ad_1", "my-plugin"}
	for _, id := range ok {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	bad := []string{"", ".", "..", "../x", "a/b", ".hidden", "has space", "bad.py"}
	for _, id := range bad {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true, want false", id)
		}
	}
}

func TestListFromDiscoversPlugins(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "divar", `{
		"name": "Divar Ads",
		"description": "Extract ads",
		"version": "1.0.0"
	}`, "after_crawl.py", "after_job.py")
	writePlugin(t, root, "onlycrawl", `{"name":"Only Crawl"}`, "after_crawl.py", "")

	if err := os.MkdirAll(filepath.Join(root, ".venv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ListFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d plugins: %+v", len(got), got)
	}
	if got[0].ID != "divar" || got[1].ID != "onlycrawl" {
		t.Fatalf("order/ids: %+v", got)
	}
	if got[0].Name != "Divar Ads" || !got[0].HasAfterCrawl || !got[0].HasAfterJob {
		t.Fatalf("divar: %+v", got[0])
	}
	if !got[1].HasAfterCrawl || got[1].HasAfterJob {
		t.Fatalf("onlycrawl hooks: %+v", got[1])
	}
}

func TestGetFromAndHookPaths(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "divar", `{"name":"Divar","after_crawl":"../escape.py"}`, "escape.py", "after_job.py")

	p, err := GetFrom(root, "divar")
	if err != nil {
		t.Fatal(err)
	}
	if p.AfterCrawl != "escape.py" {
		t.Fatalf("path traversal should be basenamed, got %q", p.AfterCrawl)
	}
	if filepath.Dir(p.AfterCrawlPath) != p.Dir {
		t.Fatalf("script escaped plugin dir: %s vs %s", p.AfterCrawlPath, p.Dir)
	}

	if _, err := GetFrom(root, "../divar"); err == nil {
		t.Fatal("expected error for invalid id")
	}
	if _, err := GetFrom(root, "missing"); err == nil {
		t.Fatal("expected error for missing plugin.json")
	}
}

func TestCustomHookFilenames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "custom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{
		"name": "Custom",
		"after_crawl": "crawl_main.py",
		"after_job": "job_main.py"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crawl_main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job_main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := GetFrom(root, "custom")
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasAfterCrawl || p.AfterCrawl != "crawl_main.py" {
		t.Fatalf("after_crawl: %+v", p)
	}
	if !p.HasAfterJob || p.AfterJob != "job_main.py" {
		t.Fatalf("after_job: %+v", p)
	}
}

func TestListFindsBundledDivarPlugin(t *testing.T) {
	root := "."
	if _, err := os.Stat(filepath.Join(root, "divar", "plugin.json")); err != nil {
		t.Skip("bundled divar plugin not present")
	}
	p, err := GetFrom(root, "divar")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name == "" || !p.HasAfterCrawl || !p.HasAfterJob {
		t.Fatalf("bundled divar plugin incomplete: %+v", p)
	}
	if filepath.Base(p.Dir) != "divar" {
		t.Fatalf("dir=%s", p.Dir)
	}
}
