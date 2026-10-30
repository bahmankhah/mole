// Package cmd implements the mole command-line interface.
package cmd

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/jobs"
	"github.com/spf13/cobra"
)

// Build information, set with -ldflags "-X github.com/resolver/crawler/cmd.version=..."
var (
	version = "dev"
	commit  = ""
)

type globalFlags struct {
	configPath string
	output     string
	verbose    bool
}

var flags globalFlags

// annotationServer marks long-running commands that always log.
const annotationServer = "mole/server"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mole",
		Short: "Mole — crawler, scraper and change-monitoring engine",
		Long: `Mole crawls websites, renders JavaScript pages in a headless browser, converts
pages to Markdown, extracts structured data (CSS/XPath/LLM), detects changes
between runs and delivers results through a REST API, webhooks and exports.

Running "mole" without a command starts the server (same as "mole serve").`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			jobs.Version = version
			log.SetFlags(log.LstdFlags)
			if flags.verbose {
				log.SetFlags(log.LstdFlags | log.Lshortfile)
			} else if cmd.Annotations[annotationServer] == "" {
				log.SetOutput(io.Discard) // one-shot commands print results, not engine logs
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(serveOptions{ui: true, worker: true})
		},
		Annotations: map[string]string{annotationServer: "true"},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&flags.configPath, "config", "c", "", "config file (default $CONFIG_PATH or ./config.yaml)")
	pf.StringVarP(&flags.output, "output", "o", "table", "output format: table or json")
	pf.BoolVarP(&flags.verbose, "verbose", "v", false, "show engine logs")

	root.AddCommand(
		newServeCmd(),
		newWorkerCmd(),
		newScrapeCmd(),
		newMapCmd(),
		newCrawlCmd(),
		newJobsCmd(),
		newSchedulesCmd(),
		newOwnersCmd(),
		newMigrateCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newPluginsCmd(),
		newJoinCmd(),
		newClusterCmd(),
		newVersionCmd(),
	)
	return root
}

// Execute runs the CLI and exits with a non-zero status on error.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	return app.LoadConfig(flags.configPath)
}

// openApp builds the application with a database connection.
func openApp(migrate bool) (*app.App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return app.New(cfg, app.Options{WithDB: true, Migrate: migrate})
}
