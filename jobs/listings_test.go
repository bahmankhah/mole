package jobs

import (
	"testing"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
)

func TestObservationMoved(t *testing.T) {
	cases := []struct {
		name string
		prev models.FieldObservation
		cur  content.FieldValue
		want bool
	}{
		{"same number", models.FieldObservation{NumValue: num(100)}, content.FieldValue{Num: num(100)}, false},
		{"number moved", models.FieldObservation{NumValue: num(100)}, content.FieldValue{Num: num(90)}, true},
		{"number appeared", models.FieldObservation{}, content.FieldValue{Num: num(90)}, true},
		{"number vanished", models.FieldObservation{NumValue: num(100)}, content.FieldValue{}, true},
		{"same text", models.FieldObservation{TextValue: "in_stock"}, content.FieldValue{Text: "in_stock"}, false},
		{"text moved", models.FieldObservation{TextValue: "in_stock"}, content.FieldValue{Text: "out_of_stock"}, true},
	}
	for _, c := range cases {
		if got := observationMoved(c.prev, c.cur); got != c.want {
			t.Errorf("%s: moved = %v, want %v", c.name, got, c.want)
		}
	}
}

// A price written 100.0 and 100 has not moved; recording a change for it
// would put a phantom step in every history.
func TestObservationMovedComparesNumbersNotText(t *testing.T) {
	prev := models.FieldObservation{NumValue: num(42000), TextValue: "42000.0"}
	if observationMoved(prev, content.FieldValue{Num: num(42000), Text: "42000"}) {
		t.Error("the same number formatted differently is not a change")
	}
}

// Compaction must be safe to re-run: a pass interrupted after writing a day
// but before deleting its raw rows has to merge rather than double-count.
func TestMergeRolledDayKeepsTheSpanAndSumsSamples(t *testing.T) {
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	stored := models.FieldObservationDay{
		Day: day, OpenValue: num(100), CloseValue: num(110),
		LowValue: num(95), HighValue: num(115), Samples: 3, Currency: "IRR",
	}
	fresh := models.FieldObservationDay{
		Day: day, OpenValue: num(108), CloseValue: num(120),
		LowValue: num(90), HighValue: num(130), Samples: 2, TextValue: "in_stock",
	}
	got := mergeRolledDay(stored, fresh)
	if *got.OpenValue != 100 {
		t.Errorf("open = %v, want the earlier 100", *got.OpenValue)
	}
	if *got.CloseValue != 120 {
		t.Errorf("close = %v, want the later 120", *got.CloseValue)
	}
	if *got.LowValue != 90 || *got.HighValue != 130 {
		t.Errorf("span = %v..%v, want 90..130", *got.LowValue, *got.HighValue)
	}
	if got.Samples != 5 {
		t.Errorf("samples = %d, want 5", got.Samples)
	}
	if got.Currency != "IRR" {
		t.Errorf("currency = %q, want the stored IRR kept", got.Currency)
	}
	if got.TextValue != "in_stock" {
		t.Errorf("text = %q, want the fresh day's value", got.TextValue)
	}
}

// A stored day with no numbers at all should take the fresh day's.
func TestMergeRolledDayAdoptsMissingValues(t *testing.T) {
	got := mergeRolledDay(
		models.FieldObservationDay{Samples: 1},
		models.FieldObservationDay{OpenValue: num(7), CloseValue: num(9), LowValue: num(7), HighValue: num(9), Samples: 1},
	)
	if got.OpenValue == nil || *got.OpenValue != 7 {
		t.Errorf("open = %v, want 7", got.OpenValue)
	}
	if got.LowValue == nil || *got.LowValue != 7 || got.HighValue == nil || *got.HighValue != 9 {
		t.Errorf("span = %v..%v, want 7..9", got.LowValue, got.HighValue)
	}
}
