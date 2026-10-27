package cmd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/spf13/cobra"
)

func newCrawlCmd() *cobra.Command {
	var ff fetchFlags
	var depth, maxPages, priority, maxDuration int
	var formats []string
	var mainOnly, detach bool
	var compareWith, owner, externalID, region, webhook, webhookSecret, extractFile, exportFormat, outFile string
	var include, exclude []string
	c := &cobra.Command{
		Use:   "crawl <url>",
		Short: "Crawl a site and wait for it to finish (or queue it with --detach)",
		Long: `Creates a crawl job. By default this process runs the job itself and shows
progress until it finishes; Ctrl+C pauses it (resume with "mole jobs resume").
With --detach the job is only queued and a running "mole serve" or
"mole worker" picks it up.`,
		Example: `  mole crawl https://example.com --depth 2 --max-pages 200 -f markdown --export jsonl --out site.jsonl
  mole crawl shop.ir --include '/product/' --extract fields.json --export csv --out products.csv
  mole crawl news.ir --detach --owner team-42 --webhook https://app.example/hooks/mole`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := ff.settings()
			if err != nil {
				return err
			}
			if len(formats) > 0 {
				s.Formats = splitCSV(formats)
			}
			if mainOnly {
				s.OnlyMainContent = boolPtr(true)
			}
			if maxPages > 0 {
				s.MaxPages = &maxPages
			}
			if len(include) > 0 {
				s.URLIncludePatterns = include
			}
			if len(exclude) > 0 {
				s.URLExcludePatterns = exclude
			}
			if compareWith != "" {
				s.ChangeDetection = boolPtr(true)
				s.CompareWithJobID = &compareWith
			}
			if extractFile != "" {
				if err := readJSONFile(extractFile, &s.ExtractFields); err != nil {
					return err
				}
			}
			spec := jobs.JobSpec{URL: args[0], MaxDepth: &depth, OwnerRef: owner, ExternalID: externalID,
				Priority: priority, Region: region, Settings: s}
			if maxDuration > 0 {
				spec.Limits = &models.JobLimits{MaxDurationSec: maxDuration}
			}
			if webhook != "" {
				spec.Webhook = &models.WebhookConfig{URL: webhook, Secret: webhookSecret}
			}

			a, err := openApp(true)
			if err != nil {
				return err
			}
			defer a.Close()
			job, err := a.Manager.CreateFromSpec(spec, true)
			if err != nil {
				return err
			}
			if detach {
				if jsonOutput() {
					return printJSON(jobs.Summarize(job))
				}
				fmt.Printf("Queued job %s\n", job.ID)
				return nil
			}

			a.Manager.StartDeliveries()
			defer a.Manager.Shutdown()
			if err := a.Manager.RunNow(job.ID); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Crawling %s (job %s) — Ctrl+C to pause\n", job.TargetURL, job.ID)
			final, err := watchJob(a.Manager, job.ID, true)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\nJob %s %s (%s): %d pages, %d requests, %s downloaded\n", final.ID, final.Status,
				orDash(final.StopReason), final.CrawledURLs, final.Usage.Requests, fmtBytes(final.Usage.Bytes))
			if exportFormat != "" && final.Status.IsTerminal() {
				return exportJob(a.Manager, final.ID, exportFormat, outFile, false)
			}
			return nil
		},
	}
	ff.register(c)
	fl := c.Flags()
	fl.IntVarP(&depth, "depth", "d", 10, "maximum link depth")
	fl.IntVar(&maxPages, "max-pages", 0, "stop after this many pages (0 = unlimited)")
	fl.IntVar(&maxDuration, "max-duration", 0, "stop after this many seconds (0 = unlimited)")
	fl.StringSliceVarP(&formats, "formats", "f", nil, "per-page outputs: markdown, html, text, links, metadata")
	fl.BoolVar(&mainOnly, "main-only", false, "convert only the main content")
	fl.StringArrayVar(&include, "include", nil, "only crawl URLs matching this regex; repeatable")
	fl.StringArrayVar(&exclude, "exclude", nil, "skip URLs matching this regex; repeatable")
	fl.StringVar(&extractFile, "extract", "", "JSON file with extraction fields")
	fl.StringVar(&compareWith, "compare-with", "", "record new/changed/removed pages against this earlier job id")
	fl.StringVar(&owner, "owner", "", "owner reference (tenant id)")
	fl.StringVar(&externalID, "external-id", "", "your own id for the job")
	fl.IntVar(&priority, "priority", 0, "queue priority (higher runs first)")
	fl.StringVar(&region, "region", "", `worker region the job must run in ("auto" = from proxy routes)`)
	fl.StringVar(&webhook, "webhook", "", "webhook URL for job events")
	fl.StringVar(&webhookSecret, "webhook-secret", "", "HMAC secret for webhook signatures")
	fl.BoolVar(&detach, "detach", false, "only queue the job")
	fl.StringVar(&exportFormat, "export", "", "export results when done: json, jsonl or csv")
	fl.StringVar(&outFile, "out", "", "export file (default stdout)")
	return c
}

// watchJob polls a job until it stops running, printing progress to stderr.
// When pauseOnInterrupt is set, Ctrl+C pauses the job and waits for it to stop.
func watchJob(m *jobs.Manager, jobID string, pauseOnInterrupt bool) (*models.CrawlJob, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	start := time.Now()
	paused := false
	for {
		job, err := m.GetJob(jobID)
		if err != nil {
			return nil, err
		}
		crawled := job.CrawledURLs
		if st, ok := m.Engine().RunStatsFor(jobID); ok {
			crawled = int(st.Crawled)
		}
		fmt.Fprintf(os.Stderr, "\r%-9s %6d pages  %6d known  %6d requests  %s   ", job.Status, crawled, job.TotalURLs,
			job.Usage.Requests, time.Since(start).Round(time.Second))
		if job.Status != models.JobStatusRunning && job.Status != models.JobStatusQueued {
			return job, nil
		}
		select {
		case <-sig:
			if !pauseOnInterrupt {
				return job, nil
			}
			if paused {
				return job, fmt.Errorf("interrupted again; job %s is still stopping", jobID)
			}
			paused = true
			fmt.Fprintf(os.Stderr, "\nPausing… (resume with: mole jobs resume %s)\n", jobID)
			if err := m.PauseJob(jobID); err != nil {
				return job, err
			}
		case <-ticker.C:
		}
	}
}

func exportJob(m *jobs.Manager, jobID, format, outFile string, onlySuccessful bool) error {
	var w io.Writer = os.Stdout
	if outFile != "" {
		f, err := os.Create(outFile)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := m.Export(jobID, format, onlySuccessful, w); err != nil {
		return err
	}
	if outFile != "" {
		fmt.Fprintf(os.Stderr, "Exported to %s\n", outFile)
	}
	return nil
}

func newJobsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "jobs",
		Aliases: []string{"job"},
		Short:   "List and control crawl jobs",
	}
	var status, owner string
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List jobs, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			list, _, err := a.Manager.ListJobs(jobs.JobFilter{Status: status, OwnerRef: owner, Limit: limit})
			if err != nil {
				return err
			}
			if jsonOutput() {
				out := make([]jobs.JobSummary, len(list))
				for i := range list {
					out[i] = jobs.Summarize(&list[i])
				}
				return printJSON(out)
			}
			t := newTable("ID", "STATUS", "PAGES", "TARGET", "OWNER", "CREATED")
			for _, j := range list {
				t.row(j.ID, j.Status, fmt.Sprintf("%d/%d", j.CrawledURLs, j.TotalURLs), shorten(j.TargetURL, 50), orDash(j.OwnerRef), fmtTime(&j.CreatedAt))
			}
			t.flush()
			return nil
		},
	}
	list.Flags().StringVar(&status, "status", "", "filter by status (comma-separated)")
	list.Flags().StringVar(&owner, "owner", "", "filter by owner reference")
	list.Flags().IntVar(&limit, "limit", 50, "maximum jobs")

	get := &cobra.Command{
		Use:   "get <id>",
		Short: "Show a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			j, err := a.Manager.GetJob(args[0])
			if err != nil {
				return fmt.Errorf("job %s not found", args[0])
			}
			if jsonOutput() {
				return printJSON(j)
			}
			t := newTable("FIELD", "VALUE")
			t.row("id", j.ID)
			t.row("status", j.Status)
			t.row("stop reason", orDash(j.StopReason))
			t.row("target", j.TargetURL)
			t.row("owner", orDash(j.OwnerRef))
			t.row("external id", orDash(j.ExternalID))
			t.row("priority / region", fmt.Sprintf("%d / %s", j.Priority, orDash(j.Region)))
			t.row("pages", fmt.Sprintf("%d crawled, %d known", j.CrawledURLs, j.TotalURLs))
			t.row("matches", j.FoundMatches)
			t.row("requests", j.Usage.Requests)
			t.row("downloaded", fmtBytes(j.Usage.Bytes))
			t.row("browser time", (time.Duration(j.Usage.BrowserMs) * time.Millisecond).Round(time.Second))
			t.row("llm tokens", j.Usage.LLMTokens)
			t.row("errors", j.Usage.Errors)
			t.row("created", fmtTime(&j.CreatedAt))
			t.row("started", fmtTime(j.StartedAt))
			t.row("completed", fmtTime(j.CompletedAt))
			if j.ErrorMessage != "" {
				t.row("error", j.ErrorMessage)
			}
			t.flush()
			return nil
		},
	}

	action := func(use, short string, fn func(m *jobs.Manager, id string) error) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>...",
			Short: short,
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := openApp(false)
				if err != nil {
					return err
				}
				defer a.Close()
				for _, id := range args {
					if err := fn(a.Manager, id); err != nil {
						return fmt.Errorf("%s: %w", id, err)
					}
					fmt.Printf("%s: %s\n", id, use+" ok")
				}
				return nil
			},
		}
	}

	var exportFormat, exportOut string
	var exportSuccessful bool
	export := &cobra.Command{
		Use:   "export <id>",
		Short: "Export a job's pages and extracted data",
		Example: `  mole jobs export 3f2a… --format csv --out products.csv
  mole jobs export 3f2a… --format jsonl | jq .extracted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			return exportJob(a.Manager, args[0], exportFormat, exportOut, exportSuccessful)
		},
	}
	export.Flags().StringVar(&exportFormat, "format", "jsonl", "json, jsonl or csv")
	export.Flags().StringVar(&exportOut, "out", "", "output file (default stdout)")
	export.Flags().BoolVar(&exportSuccessful, "successful", false, "only 2xx pages")

	watch := &cobra.Command{
		Use:   "watch <id>",
		Short: "Show live progress of a job until it stops",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			j, err := watchJob(a.Manager, args[0], false)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\n%s %s\n", j.ID, j.Status)
			return nil
		},
	}

	var changeType string
	changesCmd := &cobra.Command{
		Use:   "changes <id>",
		Short: "List pages that are new, changed or removed compared to the baseline run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			var all []models.PageChange
			var after uint
			for {
				list, next, err := a.Manager.ListChanges(args[0], changeType, after, 500, jsonOutput())
				if err != nil {
					return err
				}
				all = append(all, list...)
				if next == 0 {
					break
				}
				after = next
			}
			if jsonOutput() {
				return printJSON(all)
			}
			t := newTable("TYPE", "+", "-", "URL")
			for _, ch := range all {
				t.row(ch.ChangeType, ch.Added, ch.Removed, ch.URL)
			}
			t.flush()
			return nil
		},
	}
	changesCmd.Flags().StringVar(&changeType, "type", "", "new, changed or removed")

	pages := &cobra.Command{
		Use:   "pages <id>",
		Short: "List crawled pages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			list, _, err := a.Manager.ListPages(jobs.PageQuery{JobID: args[0], Limit: limit})
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(list)
			}
			t := newTable("ID", "STATUS", "VIA", "SIZE", "URL", "TITLE")
			for _, p := range list {
				t.row(p.ID, p.StatusCode, orDash(p.FetchedWith), fmtBytes(p.ContentLength), shorten(p.URL, 60), shorten(p.Title, 40))
			}
			t.flush()
			return nil
		},
	}
	pages.Flags().IntVar(&limit, "limit", 100, "maximum pages")

	c.AddCommand(list, get,
		action("start", "Queue pending or paused jobs", func(m *jobs.Manager, id string) error { return m.StartJob(id) }),
		action("pause", "Pause running or queued jobs", func(m *jobs.Manager, id string) error { return m.PauseJob(id) }),
		action("resume", "Resume paused jobs", func(m *jobs.Manager, id string) error { return m.ResumeJob(id) }),
		action("cancel", "Stop jobs for good", func(m *jobs.Manager, id string) error { return m.CancelJob(id) }),
		action("delete", "Delete jobs and all their data", func(m *jobs.Manager, id string) error { return m.DeleteJob(id) }),
		action("duplicate", "Copy jobs into new pending jobs", func(m *jobs.Manager, id string) error {
			j, err := m.DuplicateJob(id)
			if err == nil {
				fmt.Printf("%s → %s\n", id, j.ID)
			}
			return err
		}),
		export, watch, changesCmd, pages,
	)
	return c
}
