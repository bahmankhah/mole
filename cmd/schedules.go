package cmd

import (
	"fmt"
	"time"

	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/spf13/cobra"
)

func newSchedulesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "schedules",
		Aliases: []string{"schedule"},
		Short:   "Manage recurring crawls (cron) with change detection",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List schedules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			list, _, err := a.Manager.ListSchedules("", 200, 0)
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(list)
			}
			t := newTable("ID", "NAME", "CRON", "TZ", "ENABLED", "NEXT RUN", "RUNS")
			for _, s := range list {
				t.row(s.ID, shorten(s.Name, 30), s.Cron, s.Timezone, s.Enabled, fmtTime(s.NextRunAt), s.RunCount)
			}
			t.flush()
			return nil
		},
	}

	var in jobs.ScheduleInput
	var ff fetchFlags
	var depth, maxPages int
	var formats []string
	var mainOnly, disabled, noChanges bool
	var extractFile, webhook string
	create := &cobra.Command{
		Use:   "create <url>",
		Short: "Create a schedule",
		Example: `  mole schedules create https://shop.ir/category/phones --cron "0 */6 * * *" --name "Phone prices" \
      --extract fields.json --max-pages 500
  mole schedules create news.ir --cron @daily -f markdown --main-only --webhook https://app.example/hooks`,
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
			if extractFile != "" {
				if err := readJSONFile(extractFile, &s.ExtractFields); err != nil {
					return err
				}
			}
			in.Template = models.JobTemplate{TargetURL: args[0], MaxDepth: depth, Settings: s}
			if webhook != "" {
				in.Template.Webhook = &models.WebhookConfig{URL: webhook}
			}
			in.Enabled = boolPtr(!disabled)
			in.ChangeDetection = boolPtr(!noChanges)
			a, err := openApp(true)
			if err != nil {
				return err
			}
			defer a.Close()
			sch, err := a.Manager.CreateSchedule(in, true)
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(sch)
			}
			fmt.Printf("Created schedule %s; next run %s\n", sch.ID, fmtTime(sch.NextRunAt))
			return nil
		},
	}
	ff.register(create)
	fl := create.Flags()
	fl.StringVar(&in.Name, "name", "", "schedule name")
	fl.StringVar(&in.Cron, "cron", "", `cron expression ("0 3 * * *") or descriptor (@hourly, @daily, "@every 6h")`)
	fl.StringVar(&in.Timezone, "tz", "Asia/Tehran", "time zone of the cron expression")
	fl.StringVar(&in.OwnerRef, "owner", "", "owner reference")
	fl.IntVarP(&depth, "depth", "d", 10, "maximum link depth")
	fl.IntVar(&maxPages, "max-pages", 0, "page limit per run")
	fl.StringSliceVarP(&formats, "formats", "f", nil, "per-page outputs: markdown, html, text, links, metadata, product")
	fl.BoolVar(&mainOnly, "main-only", false, "convert only the main content")
	fl.StringVar(&extractFile, "extract", "", "JSON file with extraction fields")
	fl.StringVar(&webhook, "webhook", "", "webhook URL for run events")
	fl.BoolVar(&disabled, "disabled", false, "create the schedule disabled")
	fl.BoolVar(&noChanges, "no-change-detection", false, "do not compare runs")
	_ = create.MarkFlagRequired("cron")

	simple := func(use, short string, fn func(m *jobs.Manager, id string) (string, error)) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := openApp(false)
				if err != nil {
					return err
				}
				defer a.Close()
				msg, err := fn(a.Manager, args[0])
				if err != nil {
					return err
				}
				fmt.Println(msg)
				return nil
			},
		}
	}

	get := &cobra.Command{
		Use:   "get <id>",
		Short: "Show a schedule and its recent runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			s, err := a.Manager.GetSchedule(args[0])
			if err != nil {
				return fmt.Errorf("schedule %s not found", args[0])
			}
			runs, _ := a.Manager.GetScheduleJobs(s.ID, 10)
			if jsonOutput() {
				return printJSON(map[string]interface{}{"schedule": s, "recent_jobs": runs})
			}
			fmt.Printf("%s  %s\ncron %q (%s), enabled=%v, change detection=%v\ntarget %s\nnext run %s, %d runs so far\n\n",
				s.ID, s.Name, s.Cron, s.Timezone, s.Enabled, s.ChangeDetection, s.Template.TargetURL, fmtTime(s.NextRunAt), s.RunCount)
			t := newTable("JOB", "STATUS", "PAGES", "CREATED")
			for _, j := range runs {
				t.row(j.ID, j.Status, j.CrawledURLs, fmtTime(&j.CreatedAt))
			}
			t.flush()
			return nil
		},
	}

	c.AddCommand(list, get, create,
		simple("run", "Run a schedule now", func(m *jobs.Manager, id string) (string, error) {
			s, err := m.GetSchedule(id)
			if err != nil {
				return "", err
			}
			j, err := m.RunSchedule(s)
			if err != nil {
				return "", err
			}
			return "Queued job " + j.ID, nil
		}),
		simple("enable", "Enable a schedule", func(m *jobs.Manager, id string) (string, error) {
			return "enabled", m.SetScheduleEnabled(id, true)
		}),
		simple("disable", "Disable a schedule", func(m *jobs.Manager, id string) (string, error) {
			return "disabled", m.SetScheduleEnabled(id, false)
		}),
		simple("delete", "Delete a schedule (its jobs are kept)", func(m *jobs.Manager, id string) (string, error) {
			return "deleted", m.DeleteSchedule(id)
		}),
		&cobra.Command{
			Use:   "next <cron>",
			Short: "Preview the next run times of a cron expression",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				t := time.Now()
				for i := 0; i < 5; i++ {
					next, err := jobs.NextRun(args[0], "Asia/Tehran", t)
					if err != nil {
						return err
					}
					fmt.Println(next.Format("2006-01-02 15:04 MST"))
					t = next
				}
				return nil
			},
		},
	)
	return c
}
