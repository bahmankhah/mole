package cmd

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/handlers"
	"github.com/spf13/cobra"
)

type serveOptions struct {
	ui, worker bool
	addr       string
	region     string
	maxJobs    int
}

func newServeCmd() *cobra.Command {
	o := serveOptions{}
	var noUI, noWorker bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server (web UI, /api, /v1) and a crawl worker",
		Example: `  mole serve
  mole serve --addr 127.0.0.1:5050 --no-ui          # API-only engine behind a control plane
  mole serve --no-worker                            # API/UI only; crawls run on "mole worker" nodes`,
		Annotations: map[string]string{annotationServer: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ui, o.worker = !noUI, !noWorker
			return runServe(o)
		},
	}
	c.Flags().StringVar(&o.addr, "addr", "", "listen address (default server.host:server.port)")
	c.Flags().BoolVar(&noUI, "no-ui", false, "disable the web UI and its /api endpoints")
	c.Flags().BoolVar(&noWorker, "no-worker", false, "do not run crawl jobs in this process")
	c.Flags().StringVar(&o.region, "region", "", "worker region tag (overrides worker.region)")
	c.Flags().IntVar(&o.maxJobs, "max-jobs", 0, "max concurrent crawl jobs (overrides worker.max_running_jobs)")
	return c
}

func newWorkerCmd() *cobra.Command {
	var region, healthAddr string
	var maxJobs int
	var allowMismatch bool
	c := &cobra.Command{
		Use:   "worker",
		Short: "Run a crawl worker without the web UI or API",
		Long: `A worker claims queued jobs from the shared database and runs them. Run several
workers (on several machines, e.g. one with an Iranian IP and one abroad) against
the same database to scale out; pin jobs to workers with regions.

A node added with "mole join" refreshes its configuration from the control node
each time it starts, and never migrates the database (the control node does).`,
		Example: `  mole worker --region ir --max-jobs 8
  mole worker --health-addr :8081`,
		Annotations: map[string]string{annotationServer: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := prepare(region, maxJobs, &syncOptions{allowVersionMismatch: allowMismatch})
			if err != nil {
				return err
			}
			var srv *http.Server
			if healthAddr != "" {
				gin.SetMode(gin.ReleaseMode)
				r := gin.New()
				h := handlers.NewHandler(a.Manager)
				r.GET("/healthz", h.V1Health)
				srv = &http.Server{Addr: healthAddr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
				go func() {
					if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.Printf("health server: %v", err)
					}
				}()
			}
			if err := a.Manager.StartWorker(); err != nil {
				shutdown(a, srv)
				return err
			}
			waitForSignal()
			shutdown(a, srv)
			return nil
		},
	}
	c.Flags().StringVar(&region, "region", "", "worker region tag (overrides worker.region)")
	c.Flags().IntVar(&maxJobs, "max-jobs", 0, "max concurrent crawl jobs (overrides worker.max_running_jobs)")
	c.Flags().StringVar(&healthAddr, "health-addr", "", "serve /healthz on this address")
	c.Flags().BoolVar(&allowMismatch, "allow-version-mismatch", false, "start although the control node runs another version")
	return c
}

// prepare loads the configuration and builds the application. With sync set,
// a joined node first refreshes its configuration from the control node.
func prepare(region string, maxJobs int, sync *syncOptions) (*app.App, error) {
	path := app.ConfigPath(flags.configPath)
	cfg, err := app.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if sync != nil && cfg.Cluster.Joined() {
		if cfg, err = syncNodeConfig(path, cfg, *sync); err != nil {
			return nil, err
		}
	}
	if region != "" {
		cfg.Worker.Region = region
	}
	if maxJobs > 0 {
		cfg.Worker.MaxRunningJobs = maxJobs
	}
	log.Printf("Starting Mole %s (worker %s)", version, cfg.WorkerIdentity())
	// The control node owns the schema; a joined node never migrates it.
	return app.New(cfg, app.Options{WithDB: true, Migrate: !cfg.Cluster.Joined()})
}

func runServe(o serveOptions) error {
	a, err := prepare(o.region, o.maxJobs, nil)
	if err != nil {
		return err
	}
	cfg := a.Config
	if !o.ui {
		cfg.UI.Enabled = false
	}
	addr := o.addr
	if addr == "" {
		addr = net.JoinHostPort(cfg.Server.Host, cfg.Server.Port)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	if flags.verbose {
		router.Use(gin.Logger())
	}
	if cfg.UI.Enabled {
		tmpl, err := template.New("").Funcs(handlers.TemplateFuncs).ParseGlob("templates/*.html")
		if err != nil {
			a.Close()
			return fmt.Errorf("load templates (run from the project directory or use --no-ui): %w", err)
		}
		router.SetHTMLTemplate(tmpl)
	}
	handlers.Register(router, handlers.NewHandler(a.Manager), cfg)

	srv := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 15 * time.Second}
	if o.worker {
		if err := a.Manager.StartWorker(); err != nil {
			shutdown(a, nil)
			return err
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("Listening on http://%s (ui=%v, worker=%v)", addr, cfg.UI.Enabled, o.worker)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		shutdown(a, nil)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sig:
		log.Println("Shutting down… (running jobs go back to the queue)")
		shutdown(a, srv)
		return nil
	}
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down… (running jobs go back to the queue)")
}

func shutdown(a *app.App, srv *http.Server) {
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	a.Manager.Shutdown()
	a.Close()
}
