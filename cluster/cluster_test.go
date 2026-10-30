package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/config"
	"gopkg.in/yaml.v3"
)

// clearEnv keeps the developer's environment out of config loading.
func clearEnv(t *testing.T) {
	for _, k := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "MOLE_WORKER_ID",
		"MOLE_WORKER_REGION", "MOLE_MAX_RUNNING_JOBS", "MOLE_CONTROL_URL", "MOLE_JOIN_TOKEN", "MOLE_WORKER_BROWSER",
		"MOLE_API_TOKENS", "MOLE_UI_USERNAME", "MOLE_UI_PASSWORD", "MOLE_S3_ACCESS_KEY", "MOLE_S3_SECRET_KEY"} {
		t.Setenv(k, "")
	}
}

func controlConfig() *config.Config {
	c := config.DefaultConfig()
	c.Database = config.DatabaseConfig{Host: "127.0.0.1", Port: "3306", User: "mole", Password: "s3cret", DBName: "mole"}
	c.Storage.Driver = "s3"
	c.Storage.S3 = config.S3Config{Endpoint: "minio.internal:9000", Bucket: "mole", AccessKey: "ak", SecretKey: "sk"}
	c.API.Tokens = []string{"api-token"}
	c.UI = config.UIConfig{Enabled: true, Username: "admin", Password: "pw"}
	c.Worker.ID = "control-1"
	c.Worker.Region = "ir"
	c.Worker.LeaseTTL = 90 * time.Second
	c.Worker.MaxRunningPerOwner = 3
	c.Crawler.PythonPath = "/home/me/venv/bin/python3"
	c.Crawler.HeadlessScriptPath = "/home/me/mole/plugins/headless_fetch.py"
	c.Crawler.RequestTimeout = 45 * time.Second
	c.Network.ProxyPools = map[string][]string{"ir": {"http://u:p@10.0.0.5:3128"}}
	c.Network.ProxyRoutes = []config.ProxyRoute{{Match: ".ir", Pool: "ir", Region: "ir"}}
	c.LLM.APIKey = "llm-key"
	c.Cluster.NodeDatabaseHost = "10.8.0.1"
	c.Cluster.AdvertiseURL = "https://mole.example.com"
	return c
}

func TestNodeConfigAndNodeFileRoundTrip(t *testing.T) {
	clearEnv(t)
	control := controlConfig()
	n, warnings, err := NodeConfig(control, "intl")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if n.Database.Host != "10.8.0.1" || n.Database.Password != "s3cret" {
		t.Errorf("database = %+v", n.Database)
	}
	if len(n.API.Tokens) != 0 || n.UI.Enabled || n.UI.Password != "" {
		t.Errorf("node must not inherit API tokens or UI credentials: %+v %+v", n.API, n.UI)
	}
	if n.Worker.ID != "" || n.Worker.Region != "intl" {
		t.Errorf("worker = %+v", n.Worker)
	}
	if n.Crawler.PythonPath != "" || n.Crawler.HeadlessScriptPath != "" {
		t.Errorf("machine paths leaked: %q %q", n.Crawler.PythonPath, n.Crawler.HeadlessScriptPath)
	}
	if n.Cluster.NodeDatabaseHost != "" || n.Cluster.AdvertiseURL != "" {
		t.Errorf("cluster section leaked: %+v", n.Cluster)
	}
	if control.Database.Host != "127.0.0.1" || control.Worker.Region != "ir" || len(control.API.Tokens) != 1 {
		t.Fatal("NodeConfig modified the control configuration")
	}

	out, err := yaml.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	node := config.NodeSettings{ID: "tehran-2", MaxRunningJobs: 9, BrowserPoolSize: 3, Browser: &off}
	data, err := NodeFile(&JoinResponse{Version: "v1", Config: string(out)}, "https://mole.example.com", "mjt_abc", node)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
	got := config.LoadConfig(path)
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if !got.Cluster.Joined() || got.Cluster.ControlURL != "https://mole.example.com" || got.Cluster.JoinToken != "mjt_abc" {
		t.Errorf("cluster = %+v", got.Cluster)
	}
	if got.Worker.ID != "tehran-2" || got.Worker.Region != "intl" || got.Worker.MaxRunningJobs != 9 {
		t.Errorf("worker = %+v", got.Worker)
	}
	if got.Worker.Browser == nil || *got.Worker.Browser || got.Headless.PoolSize != 3 {
		t.Errorf("browser = %v pool = %d", got.Worker.Browser, got.Headless.PoolSize)
	}
	if got.Worker.LeaseTTL != 90*time.Second || got.Worker.MaxRunningPerOwner != 3 || got.Crawler.RequestTimeout != 45*time.Second {
		t.Errorf("cluster-wide settings lost: lease=%v perOwner=%d timeout=%v", got.Worker.LeaseTTL, got.Worker.MaxRunningPerOwner, got.Crawler.RequestTimeout)
	}
	if got.Database.Password != "s3cret" || got.Storage.S3.SecretKey != "sk" || got.LLM.APIKey != "llm-key" {
		t.Error("secrets lost in the round trip")
	}
	if len(got.Network.ProxyPools["ir"]) != 1 || len(got.Network.ProxyRoutes) != 1 {
		t.Errorf("network = %+v", got.Network)
	}
	if ok, err := IsNodeFile(path); err != nil || !ok {
		t.Errorf("IsNodeFile = %v, %v", ok, err)
	}
	ns, err := ReadNodeSettings(path)
	if err != nil || ns.ID != "tehran-2" || ns.MaxRunningJobs != 9 {
		t.Errorf("ReadNodeSettings = %+v, %v", ns, err)
	}

	// Environment variables still win on the node.
	t.Setenv("MOLE_WORKER_REGION", "ir")
	t.Setenv("DB_HOST", "db.local")
	got = config.LoadConfig(path)
	if got.Worker.Region != "ir" || got.Database.Host != "db.local" {
		t.Errorf("env overrides ignored: region=%q host=%q", got.Worker.Region, got.Database.Host)
	}
}

func TestNodeConfigStorageAndWarnings(t *testing.T) {
	c := controlConfig()
	c.Storage.Driver = "local"
	if _, _, err := NodeConfig(c, ""); !errors.Is(err, ErrLocalStorage) {
		t.Errorf("local storage: err = %v, want ErrLocalStorage", err)
	}
	c.Cluster.AllowLocalStorage = true
	if _, _, err := NodeConfig(c, ""); err != nil {
		t.Errorf("allow_local_storage: %v", err)
	}

	c = controlConfig()
	c.Cluster.NodeDatabaseHost = ""
	c.Database.Host = "localhost"
	c.Storage.S3.Endpoint = "127.0.0.1:9000"
	_, warnings, err := NodeConfig(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %v, want database and S3 warnings", warnings)
	}
	c.Cluster.NodeS3Endpoint = "s3.internal:9000"
	c.Cluster.NodeDatabasePort = "13306"
	n, warnings, _ := NodeConfig(c, "")
	if len(warnings) != 1 || n.Storage.S3.Endpoint != "s3.internal:9000" || n.Database.Port != "13306" {
		t.Errorf("overrides: endpoint=%q port=%q warnings=%v", n.Storage.S3.Endpoint, n.Database.Port, warnings)
	}
}

func TestNodeFileRejectsInvalidConfig(t *testing.T) {
	if _, err := NodeFile(&JoinResponse{Config: "worker: [nonsense"}, "https://x", "t", config.NodeSettings{}); err == nil {
		t.Error("invalid YAML accepted")
	}
	if _, err := NodeFile(&JoinResponse{Config: "storage: {driver: ftp}"}, "https://x", "t", config.NodeSettings{}); err == nil {
		t.Error("invalid configuration accepted")
	}
}

func TestIsNodeFileForControlConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("database:\n  host: localhost\n"), 0o600)
	if ok, err := IsNodeFile(path); err != nil || ok {
		t.Errorf("IsNodeFile(control config) = %v, %v", ok, err)
	}
}

func TestValidateControlURL(t *testing.T) {
	cases := []struct {
		in, want  string
		allowHTTP bool
		ok        bool
	}{
		{"https://mole.example.com/", "https://mole.example.com", false, true},
		{"https://mole.example.com/engine/", "https://mole.example.com/engine", false, true},
		{"http://10.8.0.1:5050", "http://10.8.0.1:5050", false, true},
		{"http://100.101.1.2:5050", "http://100.101.1.2:5050", false, true},
		{"http://localhost:5050", "http://localhost:5050", false, true},
		{"http://mole.example.com", "", false, false},
		{"http://mole.example.com", "http://mole.example.com", true, true},
		{"http://8.8.8.8", "", false, false},
		{"ftp://mole.example.com", "", true, false},
		{"https://user:pw@mole.example.com", "", true, false},
		{"https://mole.example.com/?a=1", "", true, false},
		{"mole.example.com", "", true, false},
		{"https://", "", true, false},
	}
	for _, c := range cases {
		got, err := ValidateControlURL(c.in, c.allowHTTP)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ValidateControlURL(%q, %v) = %q, %v; want %q ok=%v", c.in, c.allowHTTP, got, err, c.want, c.ok)
		}
	}
}

func TestNewNodeID(t *testing.T) {
	a, b := NewNodeID("Web_Server.01"), NewNodeID("Web_Server.01")
	if a == b {
		t.Error("ids of two machines with the same host name collide")
	}
	if !strings.HasPrefix(a, "web-server-01-") {
		t.Errorf("NewNodeID = %q", a)
	}
	if id := NewNodeID("…"); !strings.HasPrefix(id, "node-") {
		t.Errorf("NewNodeID(non-ascii) = %q", id)
	}
	if id := NewNodeID(strings.Repeat("a", 100)); len(id) > 47 {
		t.Errorf("NewNodeID too long: %q", id)
	}
}

func TestInstallScript(t *testing.T) {
	script := InstallScript("https://mole.example.com/it's")
	if !strings.Contains(script, `CONTROL_URL='https://mole.example.com/it'\''s'`) {
		t.Fatal("control URL is not shell-quoted")
	}
	if strings.Contains(script, "__CONTROL_URL__") {
		t.Fatal("placeholder left in script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command(sh, "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("syntax error: %v\n%s", err, out)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(sh, append([]string{"-s", "--"}, args...)...)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("--help"); err != nil || !strings.Contains(out, "--token TOKEN") {
		t.Errorf("--help: %v\n%s", err, out)
	}
	if out, err := run(); err == nil || !strings.Contains(out, "--token is required") {
		t.Errorf("no token: %v\n%s", err, out)
	}
	if out, err := run("--token"); err == nil || !strings.Contains(out, "--token needs a value") {
		t.Errorf("missing value: %v\n%s", err, out)
	}
	if out, err := run("--bogus"); err == nil || !strings.Contains(out, "unknown option") {
		t.Errorf("unknown option: %v\n%s", err, out)
	}
}

func TestJoinCommands(t *testing.T) {
	install, join, docker := JoinCommands("https://mole.example.com", "mjt_x")
	if install != "curl -fsSL 'https://mole.example.com/cluster/install.sh' | sudo sh -s -- --token 'mjt_x'" {
		t.Errorf("install = %s", install)
	}
	if join != "mole join 'https://mole.example.com' --token 'mjt_x'" {
		t.Errorf("join = %s", join)
	}
	if !strings.Contains(docker, "MOLE_CONTROL_URL='https://mole.example.com'") || !strings.HasSuffix(docker, " worker") {
		t.Errorf("docker = %s", docker)
	}
}

func TestWritePluginBundle(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("headless_fetch.py", "print(1)")
	write("requirements.txt", "playwright")
	write("divar/plugin.json", "{}")
	write("divar/.env", "KEY=1")
	write("divar/after_crawl_data/job.json", "{}")
	write("divar/__pycache__/x.pyc", "x")
	write("divar/run.log", "x")
	write(".venv/bin/python3", "x")
	write(".cookies/a", "x")
	os.Symlink("/etc/passwd", filepath.Join(root, "link"))

	var buf bytes.Buffer
	if err := WritePluginBundle(&buf, root); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	want := []string{"divar/", "divar/.env", "divar/plugin.json", "headless_fetch.py", "requirements.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("bundle = %v, want %v", names, want)
	}
}

func TestFetchConfig(t *testing.T) {
	var gotAuth string
	var gotReq JoinRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotReq)
		switch r.URL.Path {
		case "/ok/cluster/join":
			json.NewEncoder(w).Encode(map[string]interface{}{"data": JoinResponse{Version: "v2", Config: "worker: {}"}})
		case "/revoked/cluster/join":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"join token is invalid, expired or revoked"}}`))
		case "/broken/cluster/join":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	resp, err := FetchConfig(ctx, srv.URL+"/ok", "mjt_t", JoinRequest{WorkerID: "n1"})
	if err != nil || resp.Version != "v2" {
		t.Fatalf("ok: %+v, %v", resp, err)
	}
	if gotAuth != "Bearer mjt_t" || gotReq.WorkerID != "n1" {
		t.Errorf("request: auth=%q body=%+v", gotAuth, gotReq)
	}
	var rej *RejectedError
	if _, err := FetchConfig(ctx, srv.URL+"/revoked", "x", JoinRequest{}); !errors.As(err, &rej) || rej.Status != 401 || rej.Code != "unauthorized" {
		t.Errorf("revoked: %v", err)
	}
	if _, err := FetchConfig(ctx, srv.URL+"/nothing", "x", JoinRequest{}); !errors.As(err, &rej) || rej.Status != 404 {
		t.Errorf("not found: %v", err)
	}
	if _, err := FetchConfig(ctx, srv.URL+"/broken", "x", JoinRequest{}); err == nil || errors.As(err, &rej) {
		t.Errorf("5xx must be a retryable error, got %v", err)
	}
	if _, err := FetchConfig(ctx, "http://127.0.0.1:1", "x", JoinRequest{}); err == nil || errors.As(err, &rej) {
		t.Errorf("unreachable must be a retryable error, got %v", err)
	}
}
