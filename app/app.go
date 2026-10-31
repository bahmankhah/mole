// Package app wires configuration, database, storage, engine and job manager
// together for the server, the worker and the CLI commands.
package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/storage"
)

// App holds the long-lived components.
type App struct {
	Config  *config.Config
	DB      *database.Database // nil for database-less commands (scrape, map)
	Store   storage.Store
	Engine  *crawler.Engine
	Manager *jobs.Manager // nil when DB is nil
}

// ConfigPath resolves the configuration file: path, then $CONFIG_PATH, then
// config.yaml.
func ConfigPath(path string) string {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = "config.yaml"
	}
	return path
}

// LoadConfig reads the configuration file (see ConfigPath) and validates it.
func LoadConfig(path string) (*config.Config, error) {
	path = ConfigPath(path)
	cfg := config.LoadConfig(path)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Options control what New sets up.
type Options struct {
	WithDB  bool // connect to MySQL and build the job manager
	Migrate bool // run schema migrations (requires WithDB)
}

// New builds the application.
func New(cfg *config.Config, opts Options) (*App, error) {
	a := &App{Config: cfg}
	if opts.WithDB {
		db, err := database.New(cfg.Database)
		if err != nil {
			return nil, err
		}
		a.DB = db
		if opts.Migrate {
			if err := db.AutoMigrate(); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrations: %w", err)
			}
		}
	}
	store, err := storage.New(cfg.Storage)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}
	a.Store = store

	deps := crawler.Deps{Config: cfg, Store: store, WorkerID: cfg.WorkerIdentity()}
	if a.DB != nil {
		deps.DB = a.DB.DB
	}
	eng, err := crawler.NewEngine(deps)
	if err != nil {
		a.Close()
		return nil, err
	}
	a.Engine = eng
	if a.DB != nil {
		a.Manager = jobs.NewManager(a.DB.DB, cfg, eng)
		if opts.Migrate {
			// Comparisons are now schedules with several start URLs. This
			// needs the manager (for cron parsing), so it runs here rather
			// than with the schema migrations. A failure leaves the old
			// rows untouched for the next start to retry.
			if err := a.Manager.MigrateComparisons(); err != nil {
				log.Printf("[Migrate] Converting comparisons into schedules failed; they are unchanged and will be retried on the next start: %v", err)
			}
		}
	}
	return a, nil
}

// Close releases resources. Call Manager.Shutdown first when the worker runs.
func (a *App) Close() {
	if a.Manager == nil && a.Engine != nil {
		// Database-less commands still own the browser pool.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.Engine.Shutdown(ctx)
	}
	if a.DB != nil {
		if err := a.DB.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}
}
