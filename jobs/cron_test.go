package jobs

import (
	"testing"
	"time"

	"github.com/resolver/crawler/models"
)

func TestDescribeCron(t *testing.T) {
	for expr, want := range map[string]string{
		"0 3 * * *":    "Daily at 03:00",
		"30 8 * * 6":   "Weekly on Saturday at 08:30",
		"0 9 * * SAT":  "Weekly on Saturday at 09:00",
		"0 0 * * 7":    "Weekly on Sunday at 00:00",
		"15 4 1 * *":   "Monthly on day 1 at 04:15",
		"0 * * * *":    "Every hour",
		"5 * * * *":    "Every hour at :05",
		"0 */6 * * *":  "Every 6 hours",
		"10 */1 * * *": "Every hour at :10",
		"*/15 * * * *": "Every 15 minutes",
		"@daily":       "Daily at 00:00",
		"@every 90m":   "Every 1h30m0s",
		"0 3 * * 1-5":  "",
		"0 3 1 1 *":    "",
		"bogus":        "",
	} {
		if got := DescribeCron(expr); got != want {
			t.Errorf("DescribeCron(%q) = %q, want %q", expr, got, want)
		}
	}
}

func TestNextRuns(t *testing.T) {
	from := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	runs, err := NextRuns("0 3 * * *", "Asia/Tehran", from, 3)
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	for i, r := range runs {
		if r.Hour() != 3 || r.Minute() != 0 || r.Location().String() != "Asia/Tehran" || (i > 0 && r.Sub(runs[i-1]) != 24*time.Hour) {
			t.Errorf("run %d = %v", i, r)
		}
	}
	if _, err := NextRuns("61 * * * *", "UTC", from, 2); err == nil {
		t.Error("invalid cron accepted")
	}
}

func TestScheduleInputFromJob(t *testing.T) {
	yes, base := true, "old-job"
	job := &models.CrawlJob{
		ID: "j1", Kind: models.JobKindCrawl, Domain: "shop.ir", OwnerRef: "team-1",
		TargetURL: "https://shop.ir/c?page={{PAGE}}",
		SeedURLs:  models.StringSlice{"https://shop.ir/c?page=1", "https://shop.ir/c?page=2"},
		MaxDepth:  0, Priority: 5, Region: "ir",
		Settings: &models.JobSettings{ChangeDetection: &yes, CompareWithJobID: &base, Formats: []string{"product"}},
		Webhook:  &models.WebhookConfig{URL: "https://app.ir/hook", Secret: "s3cret"},
	}
	in, err := ScheduleInputFromJob(job)
	if err != nil {
		t.Fatal(err)
	}
	tp := in.Template
	if tp.TargetURL != "https://shop.ir/c?page=1" || len(tp.SeedURLs) != 1 || tp.SeedURLs[0] != "https://shop.ir/c?page=2" {
		t.Errorf("urls = %q %v (the expanded seeds replace the {{PAGE}} target)", tp.TargetURL, tp.SeedURLs)
	}
	if tp.Settings.MaxDepth == nil || *tp.Settings.MaxDepth != 0 {
		t.Errorf("max depth 0 must be kept explicitly: %v", tp.Settings.MaxDepth)
	}
	if tp.Settings.ChangeDetection != nil || tp.Settings.CompareWithJobID != nil || !*in.ChangeDetection {
		t.Error("change detection belongs to the schedule, not to the template")
	}
	if tp.Webhook.Secret != SecretMask || job.Webhook.Secret != "s3cret" || in.SourceJobID != "j1" {
		t.Errorf("webhook secret = %q (masked, source job kept), job secret = %q", tp.Webhook.Secret, job.Webhook.Secret)
	}
	if *job.Settings.ChangeDetection != true || job.Settings.CompareWithJobID == nil {
		t.Error("the job's own settings were modified")
	}
	if in.OwnerRef != "team-1" || tp.Priority != 5 || tp.Region != "ir" || tp.Settings.Formats[0] != "product" {
		t.Errorf("copied fields = %+v", in)
	}

	batch := &models.CrawlJob{ID: "b1", Kind: models.JobKindBatch, TargetURL: "https://a.ir/1",
		SeedURLs: models.StringSlice{"https://a.ir/1", "https://a.ir/2"}}
	in, _ = ScheduleInputFromJob(batch)
	if in.Template.TargetURL != "https://a.ir/1" || len(in.Template.SeedURLs) != 1 || in.Template.Settings != nil {
		t.Errorf("batch template = %+v", in.Template)
	}
}
