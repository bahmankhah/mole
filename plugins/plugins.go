// Package plugins discovers after-crawl / after-job plugins from the plugins folder.
//
// A plugin is a subdirectory of plugins/ that contains plugin.json. Optional
// Python entry points default to after_crawl.py and after_job.py:
//
//	plugins/
//	  <plugin-id>/
//	    plugin.json
//	    after_crawl.py   # optional
//	    after_job.py     # optional
package plugins

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	metaFileName          = "plugin.json"
	defaultAfterCrawlFile = "after_crawl.py"
	defaultAfterJobFile   = "after_job.py"
	maxIDLen              = 64
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Plugin is a discovered plugins/ plugin.
type Plugin struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Version        string `json:"version"`
	Dir            string `json:"-"`
	HasAfterCrawl  bool   `json:"has_after_crawl"`
	HasAfterJob    bool   `json:"has_after_job"`
	AfterCrawl     string `json:"after_crawl,omitempty"`
	AfterJob       string `json:"after_job,omitempty"`
	AfterCrawlPath string `json:"-"`
	AfterJobPath   string `json:"-"`
}

type pluginMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	AfterCrawl  string `json:"after_crawl"`
	AfterJob    string `json:"after_job"`
}

// ValidID reports whether id is a safe plugin folder name (no path separators).
func ValidID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	return idPattern.MatchString(id)
}

// ResolveDir locates the project plugins/ directory.
func ResolveDir() (string, error) {
	candidates := []string{"plugins", "scripts"} // scripts/ is the pre-rename name
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, "plugins"), filepath.Join(exeDir, "scripts"))
	}
	if wd, err := os.Getwd(); err == nil {
		// go test ./plugins runs with cwd inside this package.
		if filepath.Base(wd) == "plugins" {
			candidates = append(candidates, wd)
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return candidate, nil
		}
		return abs, nil
	}
	wd, _ := os.Getwd()
	return "", fmt.Errorf("plugins directory not found (checked relative to working directory %q and executable directory)", wd)
}

// List returns every valid plugin under plugins/.
func List() ([]Plugin, error) {
	dir, err := ResolveDir()
	if err != nil {
		return nil, err
	}
	return ListFrom(dir)
}

// ListFrom discovers plugins under root.
func ListFrom(root string) ([]Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read plugins dir %q: %w", root, err)
	}

	var out []Plugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "__pycache__" {
			continue
		}
		if !ValidID(name) {
			continue
		}
		p, err := loadPlugin(filepath.Join(root, name), name)
		if err != nil {
			log.Printf("[Plugins] skipping %s: %v", name, err)
			continue
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns the plugin with the given id from plugins/.
func Get(id string) (*Plugin, error) {
	dir, err := ResolveDir()
	if err != nil {
		return nil, err
	}
	return GetFrom(dir, id)
}

// GetFrom returns the plugin with the given id under root.
func GetFrom(root, id string) (*Plugin, error) {
	id = strings.TrimSpace(id)
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid plugin id %q", id)
	}
	p, err := loadPlugin(filepath.Join(root, id), id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("plugin %q not found", id)
	}
	return p, nil
}

// AfterCrawlScript returns the absolute path of a plugin's after-crawl entry point.
func AfterCrawlScript(id string) (string, error) {
	p, err := Get(id)
	if err != nil {
		return "", err
	}
	if !p.HasAfterCrawl || p.AfterCrawlPath == "" {
		return "", fmt.Errorf("plugin %q has no after_crawl script", id)
	}
	return p.AfterCrawlPath, nil
}

// AfterJobScript returns the absolute path of a plugin's after-job entry point.
func AfterJobScript(id string) (string, error) {
	p, err := Get(id)
	if err != nil {
		return "", err
	}
	if !p.HasAfterJob || p.AfterJobPath == "" {
		return "", fmt.Errorf("plugin %q has no after_job script", id)
	}
	return p.AfterJobPath, nil
}

func loadPlugin(dir, id string) (*Plugin, error) {
	metaPath := filepath.Join(dir, metaFileName)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var meta pluginMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaFileName, err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	p := &Plugin{
		ID:          id,
		Name:        strings.TrimSpace(meta.Name),
		Description: strings.TrimSpace(meta.Description),
		Version:     strings.TrimSpace(meta.Version),
		Dir:         absDir,
	}
	if p.Name == "" {
		p.Name = id
	}

	p.AfterCrawl, p.AfterCrawlPath, p.HasAfterCrawl = resolveHook(absDir, meta.AfterCrawl, defaultAfterCrawlFile)
	p.AfterJob, p.AfterJobPath, p.HasAfterJob = resolveHook(absDir, meta.AfterJob, defaultAfterJobFile)
	return p, nil
}

func resolveHook(pluginDir, specified, fallback string) (name, absPath string, ok bool) {
	file := strings.TrimSpace(specified)
	if file == "" {
		file = fallback
	}
	file = filepath.Base(file)
	if file == "." || file == string(filepath.Separator) || file == "" {
		return "", "", false
	}
	path := filepath.Join(pluginDir, file)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return file, abs, true
}
