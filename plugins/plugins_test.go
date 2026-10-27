package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestEnvFileAndCommand(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p1", `{"name":"P1","env":["API_KEY","API_URL"]}`, "after_crawl.py", "after_job.py")
	p, err := GetFrom(root, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MissingEnv) != 2 || p.HasEnvFile {
		t.Fatalf("missing=%v hasEnv=%v", p.MissingEnv, p.HasEnvFile)
	}
	if _, err := p.Command(context.Background(), "python3", HookAfterJob, "job-1"); err == nil {
		t.Fatal("command with missing env must fail")
	}

	env := "# comment\nexport API_KEY=\"se cret\"\nAPI_URL='http://x'\n\nMULTI=\"a\\nb\"\n"
	if err := os.WriteFile(filepath.Join(root, "p1", EnvFileName), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PASSWORD", "engine-secret")
	t.Setenv("API_URL", "from-engine")
	p, err = GetFrom(root, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MissingEnv) != 0 || !p.HasEnvFile {
		t.Fatalf("missing=%v hasEnv=%v", p.MissingEnv, p.HasEnvFile)
	}
	cmd, err := p.Command(context.Background(), "python3", HookAfterJob, "job-1", "--job-id", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range cmd.Env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate env %s", k)
		}
		got[k] = v
	}
	if got["API_KEY"] != "se cret" || got["API_URL"] != "http://x" || got["MULTI"] != "a\nb" {
		t.Fatalf("env file values wrong: %q %q %q", got["API_KEY"], got["API_URL"], got["MULTI"])
	}
	if _, leaked := got["DB_PASSWORD"]; leaked {
		t.Fatal("engine secret leaked to plugin")
	}
	if got["MOLE_JOB_ID"] != "job-1" || got["MOLE_PLUGIN_HOOK"] != HookAfterJob || got["MOLE_PLUGIN_ID"] != "p1" {
		t.Fatalf("context vars wrong: %v", got)
	}
	if cmd.Dir != p.Dir || !strings.HasSuffix(cmd.Args[1], "after_job.py") || cmd.Args[2] != "--job-id" {
		t.Fatalf("cmd %v dir %s", cmd.Args, cmd.Dir)
	}

	if err := os.WriteFile(filepath.Join(root, "p1", EnvFileName), []byte("not valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GetFrom(root, "p1"); err == nil {
		t.Fatal("malformed .env accepted")
	}
}
