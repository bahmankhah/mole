package cmd

import (
	"fmt"

	"github.com/resolver/crawler/jobs"
	"github.com/spf13/cobra"
)

// newURLsCmd builds the "urls" subcommand shared by schedules and crawls.
// Which sites a crawl covers is the thing most likely to change after it
// exists, so it is editable on its own rather than by replacing the whole
// definition.
//
// removable is false for a crawl: pages it has already fetched are part of
// its result, so a site can be added to one but not taken back out.
func newURLsCmd(what string, removable bool) *cobra.Command {
	c := &cobra.Command{
		Use:   "urls <id>",
		Short: "List, add and remove the start URLs of a " + what,
		Long: "Each start URL is one site the " + what + " covers.\n" +
			"Give several and one run spans several sites, which is what the comparison table compares.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			var urls []string
			if what == "schedule" {
				urls, err = a.Manager.ScheduleURLs(args[0])
			} else {
				urls, err = a.Manager.JobURLs(args[0])
			}
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(map[string]interface{}{"urls": urls})
			}
			t := newTable("#", "START URL")
			for i, u := range urls {
				t.row(i+1, u)
			}
			t.flush()
			return nil
		},
	}

	add := &cobra.Command{
		Use:     "add <id> <url>...",
		Short:   "Add sites to a " + what,
		Example: "  mole " + what + "s urls add " + exampleID(what) + " https://shop-b.ir/phones https://shop-c.ir/mobile",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			var res *jobs.URLChange
			if what == "schedule" {
				res, err = a.Manager.AddScheduleURLs(args[0], args[1:])
			} else {
				res, err = a.Manager.AddJobURLs(args[0], args[1:])
			}
			if err != nil {
				return err
			}
			return printURLChange(res)
		},
	}
	c.AddCommand(add)

	if removable {
		c.AddCommand(&cobra.Command{
			Use:     "rm <id> <url>...",
			Aliases: []string{"remove"},
			Short:   "Stop a " + what + " crawling the given sites",
			Long: "What those sites have already recorded is kept — their pages, and the price\n" +
				"history of their listings, stay readable. They simply stop being visited.",
			Args: cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := openApp(false)
				if err != nil {
					return err
				}
				defer a.Close()
				res, err := a.Manager.RemoveScheduleURLs(args[0], args[1:])
				if err != nil {
					return err
				}
				return printURLChange(res)
			},
		})
	}
	return c
}

func exampleID(what string) string {
	if what == "schedule" {
		return "<schedule-id>"
	}
	return "<crawl-id>"
}

// printURLChange reports what changed and, because the answer depends on
// whether the crawl is running, when it takes effect.
func printURLChange(res *jobs.URLChange) error {
	if jsonOutput() {
		return printJSON(res)
	}
	for _, u := range res.Added {
		fmt.Printf("  + %s\n", u)
	}
	for _, u := range res.Removed {
		fmt.Printf("  - %s\n", u)
	}
	for _, u := range res.Skipped {
		fmt.Printf("  = %s (already on the list)\n", u)
	}
	fmt.Printf("\n%s\n", res.Effect)
	fmt.Printf("%d start URL(s) now.\n", len(res.URLs))
	return nil
}
