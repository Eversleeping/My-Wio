package schedule

import (
	"testing"
	"time"
)

func TestNextCronUsesTimezone(t *testing.T) {
	after := time.Date(2026, 7, 30, 0, 30, 0, 0, time.UTC)
	next, err := Next("0 9 * * 1-5", "Asia/Shanghai", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next run = %s, want %s", next, want)
	}
}

func TestNextEvery(t *testing.T) {
	after := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	next, err := Next("@every 90m", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := after.Add(90 * time.Minute); !next.Equal(want) {
		t.Fatalf("next run = %s, want %s", next, want)
	}
}

func TestNextCronUsesStandardDayOfMonthOrWeekdaySemantics(t *testing.T) {
	after := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	next, err := Next("0 9 1 * 5", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	// July 31 is Friday; the day-of-month and weekday fields are both
	// restricted, so either one matching is sufficient.
	want := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next run = %s, want %s", next, want)
	}
}

func TestParseRejectsInvalidSchedule(t *testing.T) {
	for _, expression := range []string{"", "0 0 * *", "@every 30s", "61 * * * *"} {
		if _, err := Parse(expression); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", expression)
		}
	}
}
