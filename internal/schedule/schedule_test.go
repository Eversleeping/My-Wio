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

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

// runs returns every scheduled instant in [from, until).
func runs(t *testing.T, expression, timezone string, from, until time.Time) []time.Time {
	t.Helper()
	var out []time.Time
	cursor := from.Add(-time.Minute)
	for {
		next, err := Next(expression, timezone, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if !next.Before(until) {
			return out
		}
		out = append(out, next)
		cursor = next
	}
}

func assertRuns(t *testing.T, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("run %d = %s, want %s (all: %v)", i, got[i], want[i].UTC(), got)
		}
	}
}

func TestNextFixedTimeRunsOnceWhenClockFallsBack(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	// 2026-11-01 01:00-01:59 occurs twice in New York (EDT, then EST).
	from := time.Date(2026, 10, 31, 12, 0, 0, 0, newYork)
	until := time.Date(2026, 11, 2, 12, 0, 0, 0, newYork)
	assertRuns(t, runs(t, "30 1 * * *", "America/New_York", from, until),
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT, first occurrence only
		time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC), // 01:30 EST
	)

	// Starting inside the repeated hour must not produce the second 01:30.
	insideRepeat := time.Date(2026, 11, 1, 6, 10, 0, 0, time.UTC) // 01:10 EST
	next, err := Next("30 1 * * *", "America/New_York", insideRepeat)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next run = %s, want %s", next, want)
	}
}

func TestNextWildcardHourRunsEveryRealHourWhenClockFallsBack(t *testing.T) {
	from := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)  // 00:00 EDT
	until := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC) // 03:00 EST
	assertRuns(t, runs(t, "30 * * * *", "America/New_York", from, until),
		time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC), // 00:30 EDT
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), // 01:30 EST
		time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC), // 02:30 EST
	)
}

func TestNextFixedTimeInSpringForwardGapRunsAfterGap(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	// 2026-03-08 02:00-02:59 does not exist in New York.
	from := time.Date(2026, 3, 7, 12, 0, 0, 0, newYork)
	until := time.Date(2026, 3, 9, 12, 0, 0, 0, newYork)
	assertRuns(t, runs(t, "30 2 * * *", "America/New_York", from, until),
		time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC),  // 03:00 EDT, first instant after the gap
		time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC), // 02:30 EDT
	)
	// A wildcard job simply has no run for the missing wall-clock times.
	assertRuns(t, runs(t, "30 * * * *", "America/New_York", time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC)),
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), // 01:30 EST
		time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), // 03:30 EDT
		time.Date(2026, 3, 8, 8, 30, 0, 0, time.UTC), // 04:30 EDT
	)
	// Jobs that exist on both sides of the gap are unaffected.
	assertRuns(t, runs(t, "0 3 * * *", "America/New_York", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)),
		time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC),
	)
}

func TestNextEuropeBerlinTransitions(t *testing.T) {
	// Berlin springs forward 2026-03-29 02:00 CET -> 03:00 CEST and falls
	// back 2026-10-25 03:00 CEST -> 02:00 CET.
	assertRuns(t, runs(t, "15 2 * * *", "Europe/Berlin", time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)),
		time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC),  // 03:00 CEST
		time.Date(2026, 3, 30, 0, 15, 0, 0, time.UTC), // 02:15 CEST
	)
	assertRuns(t, runs(t, "15 2 * * *", "Europe/Berlin", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)),
		time.Date(2026, 10, 25, 0, 15, 0, 0, time.UTC), // 02:15 CEST only
		time.Date(2026, 10, 26, 1, 15, 0, 0, time.UTC), // 02:15 CET
	)
	assertRuns(t, runs(t, "*/30 2 * * *", "Europe/Berlin", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)),
		time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC),
	)
}
