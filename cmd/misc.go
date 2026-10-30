package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/netx"
	"github.com/resolver/crawler/plugins"
	"github.com/resolver/crawler/search"
	"github.com/resolver/crawler/storage"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newOwnersCmd() *cobra.Command {
	c := &cobra.Command{Use: "owners", Short: "Per-owner usage and data deletion"}
	var from, to string
	usage := &cobra.Command{
		Use:   "usage <owner-ref>",
		Short: "Sum an owner's usage (default: this month)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
			end := now.Add(time.Second)
			var err error
			if from != "" {
				if start, err = time.ParseInLocation("2006-01-02", from, now.Location()); err != nil {
					return fmt.Errorf("--from: %w", err)
				}
			}
			if to != "" {
				if end, err = time.ParseInLocation("2006-01-02", to, now.Location()); err != nil {
					return fmt.Errorf("--to: %w", err)
				}
			}
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			u, err := a.Manager.GetOwnerUsage(args[0], start, end)
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(u)
			}
			t := newTable("METRIC", "VALUE")
			t.row("jobs", u.Jobs)
			t.row("pages", u.Usage.Pages)
			t.row("requests", u.Usage.Requests)
			t.row("downloaded", fmtBytes(u.Usage.Bytes))
			t.row("browser time", (time.Duration(u.Usage.BrowserMs) * time.Millisecond).Round(time.Second))
			t.row("llm tokens", u.Usage.LLMTokens)
			t.row("screenshots", u.Usage.Screenshots)
			t.row("proxy requests", u.Usage.ProxyRequests)
			t.row("document pages", u.Usage.DocumentPages)
			t.flush()
			return nil
		},
	}
	usage.Flags().StringVar(&from, "from", "", "start date YYYY-MM-DD (inclusive)")
	usage.Flags().StringVar(&to, "to", "", "end date YYYY-MM-DD (exclusive)")

	var yes bool
	del := &cobra.Command{
		Use:   "delete <owner-ref>",
		Short: "Delete every job, schedule and artifact of an owner",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Fprintf(os.Stderr, "Delete ALL data of owner %q? Type the owner ref to confirm: ", args[0])
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != args[0] {
					return fmt.Errorf("aborted")
				}
			}
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			res, err := a.Manager.DeleteOwner(args[0])
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(res)
			}
			fmt.Printf("Deleted %d jobs and %d schedules of %s\n", res.Jobs, res.Schedules, args[0])
			return nil
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	c.AddCommand(usage, del)
	return c
}

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Create or update the database schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			db, err := database.New(cfg.Database)
			if err != nil {
				return err
			}
			defer db.Close()
			if err := db.AutoMigrate(); err != nil {
				return err
			}
			fmt.Println("Database schema is up to date")
			return nil
		},
	}
}

func newPluginsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "plugins",
		Short: "Python after-crawl / after-job plugins",
		Long: `Plugins are folders under plugins/ with a plugin.json and optional
after_crawl.py (runs for every page, page JSON on stdin) and after_job.py
(runs once after the job, with --job-id). Secrets go in plugins/<id>/.env;
plugin.json "env" lists the variables a plugin requires.`,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List plugins and whether their required environment is set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := plugins.List()
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(list)
			}
			t := newTable("ID", "NAME", "HOOKS", "ENV", "DESCRIPTION")
			for _, p := range list {
				var hooks []string
				if p.HasAfterCrawl {
					hooks = append(hooks, "after_crawl")
				}
				if p.HasAfterJob {
					hooks = append(hooks, "after_job")
				}
				env := "-"
				switch {
				case len(p.MissingEnv) > 0:
					env = "missing " + strings.Join(p.MissingEnv, ",")
				case len(p.Env) > 0:
					env = "ok"
				}
				t.row(p.ID, p.Name, strings.Join(hooks, "+"), env, shorten(p.Description, 50))
			}
			t.flush()
			return nil
		},
	}
	var pluginID string
	run := &cobra.Command{
		Use:   "run-after-job <job-id>",
		Short: "Run a job's after-job plugin now in this process and print its output",
		Example: `  mole plugins run-after-job 3f2a…
  mole plugins run-after-job 3f2a… --plugin divar`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := a.Manager.RunAfterJobInline(cmd.Context(), args[0], pluginID)
			fmt.Print(out)
			return err
		},
	}
	run.Flags().StringVar(&pluginID, "plugin", "", "plugin id (default: the job's after-job plugin)")
	c.AddCommand(list, run)
	return c
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			c := commit
			if c == "" {
				c = "unknown"
			}
			fmt.Printf("mole %s (commit %s, %s, %s/%s)\n", version, c, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect the configuration"}
	c.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (secrets redacted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			redacted := *cfg
			redact := func(s string) string {
				if s == "" {
					return ""
				}
				return "********"
			}
			redacted.Database.Password = redact(cfg.Database.Password)
			redacted.UI.Password = redact(cfg.UI.Password)
			redacted.LLM.APIKey = redact(cfg.LLM.APIKey)
			redacted.Storage.S3.SecretKey = redact(cfg.Storage.S3.SecretKey)
			redacted.Cluster.JoinToken = redact(cfg.Cluster.JoinToken)
			redacted.Search.APIKey = redact(cfg.Search.APIKey)
			if cfg.Search.Proxy != "" {
				redacted.Search.Proxy = netx.RedactProxy(cfg.Search.Proxy)
			}
			redacted.API.Tokens = make([]string, len(cfg.API.Tokens))
			for i := range cfg.API.Tokens {
				redacted.API.Tokens[i] = "********"
			}
			pools := map[string][]string{}
			for name, list := range cfg.Network.ProxyPools {
				for _, p := range list {
					pools[name] = append(pools[name], netx.RedactProxy(p))
				}
			}
			redacted.Network.ProxyPools = pools
			if jsonOutput() {
				return printJSON(redacted)
			}
			out, err := yaml.Marshal(redacted)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		},
	}, &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			a, err := app.New(cfg, app.Options{}) // builds proxies, guard and storage
			if err != nil {
				return err
			}
			a.Close()
			fmt.Println("Configuration is valid")
			return nil
		},
	})
	return c
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that every dependency works (database, Python, browser, storage, LLM)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			failed := 0
			check := func(name string, fn func() (string, error)) {
				detail, err := fn()
				if err != nil {
					failed++
					fmt.Printf("✗ %-22s %v\n", name, err)
					return
				}
				fmt.Printf("✓ %-22s %s\n", name, detail)
			}
			cfg, err := loadConfig()
			check("config", func() (string, error) {
				if err != nil {
					return "", err
				}
				return "valid", nil
			})
			if err != nil {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			check("database", func() (string, error) {
				db, err := database.New(cfg.Database)
				if err != nil {
					return "", err
				}
				defer db.Close()
				sqlDB, err := db.DB.DB()
				if err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := sqlDB.PingContext(ctx); err != nil {
					return "", err
				}
				return fmt.Sprintf("%s:%s/%s reachable", cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName), nil
			})
			python := modules.FindPython(cfg.Crawler.PythonPath)
			check("python", func() (string, error) {
				out, err := exec.Command(python, "--version").CombinedOutput()
				return strings.TrimSpace(string(out)) + " (" + python + ")", err
			})
			check("playwright", func() (string, error) {
				out, err := exec.Command(python, "-c", "import playwright; print('installed')").CombinedOutput()
				if err != nil {
					return "", fmt.Errorf("not importable; run plugins/setup_python.sh (%s)", strings.TrimSpace(string(out)))
				}
				return "installed", nil
			})
			check("headless browser", func() (string, error) {
				pool := modules.NewBrowserPool(python, modules.FindPluginScript("headless_fetch.py", cfg.Crawler.HeadlessScriptPath), 1, false)
				defer pool.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				res := pool.Fetch(ctx, "about:blank", modules.FetchOptions{RenderWait: 1, Timeout: 20 * time.Second})
				if res.Error != nil {
					return "", res.Error
				}
				return "Chromium starts and renders", nil
			})
			check("storage", func() (string, error) {
				st, err := storage.New(cfg.Storage)
				if err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				key := "doctor/check.txt"
				if err := st.Put(ctx, key, []byte("ok"), "text/plain"); err != nil {
					return "", err
				}
				if _, err := st.Get(ctx, key); err != nil {
					return "", err
				}
				_ = st.DeletePrefix(ctx, "doctor/")
				return st.Driver() + " read/write ok", nil
			})
			check("proxies", func() (string, error) {
				routes := make([]netx.ProxyRoute, 0, len(cfg.Network.ProxyRoutes))
				for _, r := range cfg.Network.ProxyRoutes {
					routes = append(routes, netx.ProxyRoute{Match: r.Match, Pool: r.Pool, Region: r.Region})
				}
				m, err := netx.NewProxyManager(cfg.Network.ProxyPools, routes, cfg.Network.DefaultProxyPool)
				if err != nil {
					return "", err
				}
				if len(m.PoolNames()) == 0 {
					return "none configured (direct connections)", nil
				}
				return fmt.Sprintf("%d pool(s): %s", len(m.PoolNames()), strings.Join(m.PoolNames(), ", ")), nil
			})
			check("plugins", func() (string, error) {
				list, err := plugins.List()
				if err != nil {
					return "", err
				}
				var problems []string
				for _, p := range list {
					if len(p.MissingEnv) > 0 {
						problems = append(problems, fmt.Sprintf("%s needs %s in plugins/%s/.env", p.ID, strings.Join(p.MissingEnv, ", "), p.ID))
					}
				}
				if len(problems) > 0 {
					return "", fmt.Errorf("%s", strings.Join(problems, "; "))
				}
				return fmt.Sprintf("%d plugin(s), required environment set", len(list)), nil
			})
			check("documents", func() (string, error) {
				path := content.PDFSupport()
				if path == "" {
					return "", fmt.Errorf("pdftotext not found: install poppler-utils to convert PDFs (DOCX works without it)")
				}
				return "PDF (" + path + ") and DOCX", nil
			})
			check("search", func() (string, error) {
				p, err := search.New(cfg.Search)
				if err != nil {
					return "", err
				}
				if p == nil {
					return "not configured (/v1/search disabled)", nil
				}
				if p.Name() != "searxng" {
					return p.Name() + " configured (not queried: provider queries are billed)", nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				res, err := p.Search(ctx, search.Query{Query: "mole", Limit: 1})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("searxng answered (%d result(s))", len(res)), nil
			})
			check("llm", func() (string, error) {
				c := content.NewLLMClient(cfg.LLM)
				if c == nil {
					return "not configured (LLM extraction disabled)", nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), cfg.LLM.Timeout)
				defer cancel()
				_, tokens, err := c.Extract(ctx, `Return {"ok": true}`, "", "about:blank", "test")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%s answered (%d tokens)", c.Model(), tokens), nil
			})
			check("security", func() (string, error) {
				var warn []string
				if len(cfg.API.Tokens) == 0 {
					warn = append(warn, "api.tokens empty")
				}
				if cfg.UI.Enabled && cfg.UI.Username == "" {
					warn = append(warn, "UI has no password")
				}
				if cfg.Security.AllowPrivateNetworks {
					warn = append(warn, "private networks reachable")
				}
				if len(warn) > 0 {
					return "local-only setup: " + strings.Join(warn, ", "), nil
				}
				return "API tokens and UI password set, private networks blocked", nil
			})
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
}
