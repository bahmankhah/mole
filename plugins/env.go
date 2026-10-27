package plugins

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnvFileName is the per-plugin file holding secrets and settings
// (KEY=VALUE lines). It is gitignored; commit a .env.example instead.
const EnvFileName = ".env"

// engineSecrets are engine variables never passed to plugin processes.
var engineSecrets = map[string]bool{
	"DB_PASSWORD":        true,
	"MOLE_API_TOKENS":    true,
	"MOLE_UI_PASSWORD":   true,
	"MOLE_LLM_API_KEY":   true,
	"MOLE_S3_ACCESS_KEY": true,
	"MOLE_S3_SECRET_KEY": true,
}

// LoadEnvFile parses plugins/<id>/.env. A missing file yields an empty map.
// Lines are KEY=VALUE; blank lines and # comments are ignored, an "export "
// prefix and matching surrounding quotes are allowed.
func (p *Plugin) LoadEnvFile() (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(filepath.Join(p.Dir, EnvFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("%s/%s line %d: expected KEY=VALUE", p.ID, EnvFileName, n)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			double := val[0] == '"'
			val = val[1 : len(val)-1]
			if double {
				val = strings.ReplaceAll(val, `\n`, "\n")
			}
		}
		out[key] = val
	}
	return out, sc.Err()
}

// Hook names passed to plugins in MOLE_PLUGIN_HOOK.
const (
	HookAfterCrawl = "after_crawl"
	HookAfterJob   = "after_job"
)

// Command builds the process for one hook run: working directory is the
// plugin folder, the environment is the engine's minus its secrets, plus the
// plugin's .env file and MOLE_* context variables.
func (p *Plugin) Command(ctx context.Context, python, hook, jobID string, args ...string) (*exec.Cmd, error) {
	script := p.AfterCrawlPath
	if hook == HookAfterJob {
		script = p.AfterJobPath
	}
	if script == "" {
		return nil, fmt.Errorf("plugin %q has no %s script", p.ID, hook)
	}
	fileEnv, err := p.LoadEnvFile()
	if err != nil {
		return nil, err
	}
	if len(p.MissingEnv) > 0 {
		return nil, fmt.Errorf("plugin %q is missing environment variables %s (set them in plugins/%s/%s)",
			p.ID, strings.Join(p.MissingEnv, ", "), p.ID, EnvFileName)
	}
	env := make([]string, 0, len(os.Environ())+len(fileEnv)+4)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if engineSecrets[name] {
			continue
		}
		if _, overridden := fileEnv[name]; overridden {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range fileEnv {
		env = append(env, k+"="+v)
	}
	env = append(env,
		"MOLE_PLUGIN_ID="+p.ID,
		"MOLE_PLUGIN_DIR="+p.Dir,
		"MOLE_PLUGIN_HOOK="+hook,
		"MOLE_JOB_ID="+jobID,
	)
	cmd := exec.CommandContext(ctx, python, append([]string{script}, args...)...)
	cmd.Dir = p.Dir
	cmd.Env = env
	return cmd, nil
}
