package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/jobs"
	"github.com/spf13/cobra"
)

// The two halves of product tracking on the command line. "groups" reads
// across — who sells a product, and for how much. "listings" reads along —
// what one shop has done with its price over time.

func newGroupsCmd() *cobra.Command {
	var field string
	var page, perPage int
	c := &cobra.Command{
		Use:     "groups <crawl-or-schedule-id>",
		Aliases: []string{"compare"},
		Short:   "Compare the shops a crawl or schedule covers, product by product",
		Long: "One row per product, one column per shop. A schedule also carries each offer's\n" +
			"history; use \"mole listings\" to follow one shop over time.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			scopeType, scopeID, err := a.Manager.ResolveGroupingScope(args[0])
			if err != nil {
				return err
			}
			dash, err := a.Manager.GetGroupingDashboard(scopeType, scopeID, field, page, perPage)
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(dash)
			}
			return printDashboard(dash)
		},
	}
	c.Flags().StringVar(&field, "field", "", "which tracked field to show (default: price)")
	c.Flags().IntVar(&page, "page", 1, "page of products")
	c.Flags().IntVar(&perPage, "per-page", 25, "products per page")

	c.AddCommand(&cobra.Command{
		Use:   "rematch <crawl-or-schedule-id>",
		Short: "Match the products again from the pages already crawled",
		Long:  "Nothing is re-crawled, and no recorded price is changed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			scopeType, scopeID, err := a.Manager.ResolveGroupingScope(args[0])
			if err != nil {
				return err
			}
			res, err := a.Manager.Regroup(scopeType, scopeID, jobs.RegroupOptions{Partial: true})
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(res)
			}
			if res.Reason != "" {
				fmt.Printf("Nothing grouped: %s\n", res.Reason)
				return nil
			}
			fmt.Printf("%d offers in %d products (%d new) from %d pages\n",
				res.Candidates, res.Groups, res.Created, res.ProductPages)
			fmt.Printf("%d values recorded, %d changed, %d new offers, %d moved URL, %d merged, %d no longer listed\n",
				res.Observations, res.Changes, res.NewListings, res.URLChanges, res.Merged, res.Retired)
			return nil
		},
	})
	return c
}

func printDashboard(d *jobs.GroupingDashboard) error {
	fmt.Printf("%s · %d products · %d offers · field %q\n",
		d.ScopeType, d.TotalGroups, d.TotalListings, d.Field)
	if !d.Tracks {
		fmt.Println("One-off crawl: the shops are compared at one moment, with no history. Run it on a schedule to track changes.")
	}
	if len(d.Rows) == 0 {
		fmt.Println("\nNo matched products yet.")
		return nil
	}
	headers := []string{"PRODUCT"}
	for _, s := range d.Sites {
		headers = append(headers, shorten(s.Label, 18))
	}
	t := newTable(headers...)
	for _, row := range d.Rows {
		cols := []interface{}{shorten(labelOf(row), 40)}
		for _, s := range d.Sites {
			cols = append(cols, cellText(row.Cells[s.SiteKey], d.Tracks))
		}
		t.row(cols...)
	}
	t.flush()
	if len(d.Unmatched) > 0 {
		fmt.Printf("\n%d offer(s) not matched to a product yet.\n", len(d.Unmatched))
	}
	if d.TotalPages > 1 {
		fmt.Printf("page %d of %d\n", d.Page, d.TotalPages)
	}
	return nil
}

func labelOf(row jobs.ProductRow) string {
	if row.Label != "" {
		return row.Label
	}
	return "(untitled)"
}

// cellText renders one shop's column: its cheapest live offer, how it moved
// since the previous run, and how many offers it folds.
func cellText(cell *jobs.FieldCell, tracks bool) string {
	if cell == nil {
		return "—"
	}
	out := cell.Text
	if cell.Num != nil {
		out = thousands(*cell.Num) + " " + cell.Currency
	}
	if cell.Stale {
		return out + " (gone)"
	}
	if tracks && cell.Changed() {
		arrow := "v"
		if cell.Delta() > 0 {
			arrow = "^"
		}
		out += " " + arrow + thousands(absFloat(cell.Delta()))
	}
	if n := cell.Offers(); n > 1 {
		out += fmt.Sprintf(" (%d offers)", n)
	}
	return out
}

// thousands groups digits so a rial price is readable in a terminal
// table. --output json keeps the exact numbers for scripts.
func thousands(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func newListingsCmd() *cobra.Command {
	var field string
	c := &cobra.Command{
		Use:     "listings <listing-id>",
		Aliases: []string{"listing", "offer"},
		Short:   "Follow one shop's offer of one product over time",
		Long: "A listing is one shop's offer of one product: the cell of the comparison table,\n" +
			"and the thing a price history belongs to. Its id is shown by \"mole groups\".",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid listing id %q", args[0])
			}
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			h, err := a.Manager.GetListingHistory(uint(id), field)
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(h)
			}
			return printListingHistory(h)
		},
	}
	c.Flags().StringVar(&field, "field", "", "which tracked field to show (default: price)")

	for _, act := range []struct {
		use, short string
		fn         func(*app.App, uint) error
	}{
		{"pin <listing-id>", "Confirm this match: keep the offer in its product on future matching",
			func(a *app.App, id uint) error { return a.Manager.PinListing(id) }},
		{"unpin <listing-id>", "Let matching place the offer again",
			func(a *app.App, id uint) error { return a.Manager.UnpinListing(id) }},
		{"exclude <listing-id>", "Not this product: drop it from the comparison, keeping its history",
			func(a *app.App, id uint) error { return a.Manager.ExcludeListing(id) }},
		{"unexclude <listing-id>", "Allow the offer to be matched again",
			func(a *app.App, id uint) error { return a.Manager.UnexcludeListing(id) }},
	} {
		act := act
		c.AddCommand(&cobra.Command{
			Use: act.use, Short: act.short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := strconv.ParseUint(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("invalid listing id %q", args[0])
				}
				a, err := openApp(false)
				if err != nil {
					return err
				}
				defer a.Close()
				if err := act.fn(a, uint(id)); err != nil {
					return err
				}
				fmt.Println("Done.")
				return nil
			},
		})
	}

	c.AddCommand(&cobra.Command{
		Use:   "move <listing-id> <product-group-id>",
		Short: "Compare this offer as a different product",
		Long:  "Its history moves with it; nothing recorded is lost.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid listing id %q", args[0])
			}
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.Manager.MoveListing(uint(id), args[1]); err != nil {
				return err
			}
			fmt.Println("Moved, with its history.")
			return nil
		},
	})
	return c
}

func printListingHistory(h *jobs.ListingHistory) error {
	fmt.Printf("%s\n%s\n", shorten(h.Listing.Title, 90), h.Listing.URL)
	fmt.Printf("shop %s · %s · %d run(s)\n", h.Listing.SiteLabel, h.Field, h.Runs)
	if h.Group != nil {
		fmt.Printf("compared as %s (%s)\n", shorten(h.Group.Label, 60), h.Group.ID)
	} else {
		fmt.Println("not matched to a product yet")
	}
	if h.KeyNote != "" {
		mark := "fragile"
		if h.KeyStable {
			mark = "stable"
		}
		fmt.Printf("identity: %s — %s\n", mark, h.KeyNote)
	}
	if len(h.Listing.PreviousURLs) > 0 {
		fmt.Printf("moved %d time(s) and kept its history\n", len(h.Listing.PreviousURLs))
	}
	if !h.Tracks {
		fmt.Println("\nOne-off crawl: one value, no history. Run it on a schedule to track changes.")
	}
	if len(h.Points) > 0 {
		fmt.Println()
		t := newTable("WHEN", "VALUE", "NOTE")
		for _, p := range h.Points {
			value := p.Text
			if p.Num != nil {
				value = thousands(*p.Num) + " " + p.Currency
			}
			note := "one run"
			if p.Rolled {
				note = fmt.Sprintf("day's close, %d runs", p.Samples)
			}
			t.row(p.At.Format("2006-01-02 15:04"), value, note)
		}
		t.flush()
	}
	if len(h.Changes) > 0 {
		fmt.Printf("\n%d change(s):\n", len(h.Changes))
		t := newTable("WHEN", "FROM", "TO")
		for _, ch := range h.Changes {
			t.row(ch.DetectedAt.Format("2006-01-02 15:04"), valueOf(ch.OldNum, ch.OldText), valueOf(ch.NewNum, ch.NewText))
		}
		t.flush()
	}
	return nil
}

func valueOf(num *float64, text string) string {
	if num != nil {
		return thousands(*num)
	}
	if text == "" {
		return "—"
	}
	return text
}
