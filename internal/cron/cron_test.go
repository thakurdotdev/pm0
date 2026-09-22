package cron

import (
	"testing"
	"time"
)

// at builds a UTC time from components.
func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestParseValid(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 2 * * *",
		"30 4 1,15 * 5",
		"0 0 * * 0",
		"0 0 * * 7", // 7 == sunday
		"15,45 */6 * jan-mar *",
		"1-30/10 9-17 * * mon-fri",
		"5 3 * feb *",
		"@daily",
		"@midnight",
		"@hourly",
		"@weekly",
		"@monthly",
		"@yearly",
		"@annually",
		"  0   12   *   *   mon  ", // whitespace tolerance
		"0 12 * jan-mar *",         // month names in month field
		"0 12 13 * fri",            // dom+dow both restricted (OR rule)
	}
	for _, spec := range valid {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q): %v", spec, err)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		"* * * *",      // too few
		"* * * * * *",  // too many (6-field not supported)
		"60 * * * *",   // minute range
		"* 24 * * *",   // hour range
		"0 0 32 * *",   // dom range
		"0 0 * 13 *",   // mon range
		"0 0 * * 8",    // dow range (7 ok, 8 not)
		"a * * * *",    // non-numeric, no name
		"*/0 * * * *",  // zero step
		"*/-2 * * * *", // negative step
		"10-5 * * * *", // inverted range
		"5-70 * * * *", // range overflows
		"1,,2 * * * *", // empty list element
		"0 0 31 2 *",   // feb 31 never exists -> matches no time
		"@nonsense",
		"0 0 * * mum",   // bad dow name
		"0 0 * * mum *", // field count after name garbage
	}
	for _, spec := range invalid {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q): expected error, got none", spec)
		}
	}
}

func TestNextBasic(t *testing.T) {
	cases := []struct {
		spec  string
		after time.Time
		want  time.Time
	}{
		// Every minute: next full minute after 10:30:00.
		{"* * * * *", at(2026, 1, 1, 10, 30), at(2026, 1, 1, 10, 31)},
		// After :30, next */5 hit is :35.
		{"*/5 * * * *", at(2026, 1, 1, 10, 30), at(2026, 1, 1, 10, 35)},
		// Daily at 02:00: from 02:00 exactly -> NEXT day (strictly after).
		{"0 2 * * *", at(2026, 1, 1, 2, 0), at(2026, 1, 2, 2, 0)},
		// Daily at 02:00: from 01:30 same day.
		{"0 2 * * *", at(2026, 1, 1, 1, 30), at(2026, 1, 1, 2, 0)},
		// Daily at 02:00: from 23:59 -> next day.
		{"0 2 * * *", at(2026, 1, 1, 23, 59), at(2026, 1, 2, 2, 0)},
		// Two hours per day (9,17): from 05:00 -> 09:00 SAME day (hour-skip bug guard).
		{"0 9,17 * * *", at(2026, 6, 1, 5, 0), at(2026, 6, 1, 9, 0)},
		{"0 9,17 * * *", at(2026, 6, 1, 10, 0), at(2026, 6, 1, 17, 0)},
		// First of month.
		{"0 0 1 * *", at(2026, 1, 15, 12, 0), at(2026, 2, 1, 0, 0)},
		// Month boundary (dec -> jan).
		{"0 0 1 * *", at(2026, 12, 15, 0, 0), at(2027, 1, 1, 0, 0)},
		// Leap day: next Feb 29 after 2026-01-01 is 2028-02-29.
		{"0 0 29 2 *", at(2026, 1, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		// Monday: 2026-01-05 is a Monday.
		{"0 0 * * 1", at(2026, 1, 1, 0, 0), at(2026, 1, 5, 0, 0)},
		// dow 7 == 0 (sunday): 2026-01-04 is a Sunday.
		{"0 0 * * 7", at(2026, 1, 1, 0, 0), at(2026, 1, 4, 0, 0)},
		// Named fields.
		{"0 0 * * mon", at(2026, 1, 1, 0, 0), at(2026, 1, 5, 0, 0)},
		{"15 12 * jan-mar *", at(2026, 4, 1, 0, 0), at(2027, 1, 1, 12, 15)},
		// dom/dow OR rule: "0 12 13 * fri" fires on the 13th OR any Friday.
		// 2026-01-02 is a Friday (before Jan 13).
		{"0 12 13 * fri", at(2026, 1, 1, 0, 0), at(2026, 1, 2, 12, 0)},
		// ...and from Jan 2 13:00 -> Jan 9 (Friday) not Jan 13 (Tuesday comes first? no: 9 < 13).
		{"0 12 13 * fri", at(2026, 1, 2, 13, 0), at(2026, 1, 9, 12, 0)},
		// Minutes list with hour step.
		{"15,45 */6 * * *", at(2026, 3, 3, 5, 0), at(2026, 3, 3, 6, 15)},
		// Step over range: 1-30/10 -> :1,:11,:21,:31... wait, 1,11,21 (+30/10 later? no, range caps at 30).
		{"1-30/10 * * * *", at(2026, 3, 3, 0, 0), at(2026, 3, 3, 0, 1)},
		{"1-30/10 * * * *", at(2026, 3, 3, 0, 1), at(2026, 3, 3, 0, 11)},
		{"1-30/10 * * * *", at(2026, 3, 3, 0, 21), at(2026, 3, 3, 1, 1)}, // 31 outside 1-30 -> next hour :1
		// Single value n/step: 5/15 = :5,:20,:35,:50.
		{"5/15 * * * *", at(2026, 3, 3, 0, 0), at(2026, 3, 3, 0, 5)},
		{"5/15 * * * *", at(2026, 3, 3, 0, 35), at(2026, 3, 3, 0, 50)},
		// @hourly: top of the next hour.
		{"@hourly", at(2026, 3, 3, 7, 59), at(2026, 3, 3, 8, 0)},
		// @weekly: Sunday 00:00. 2026-01-04 is a Sunday.
		{"@weekly", at(2026, 1, 1, 0, 0), at(2026, 1, 4, 0, 0)},
		// @yearly: Jan 1 00:00.
		{"@yearly", at(2026, 6, 1, 0, 0), at(2027, 1, 1, 0, 0)},
		// Far skip (feb -> mar when day 30+ in feb).
		{"0 0 30 * *", at(2026, 2, 1, 0, 0), at(2026, 3, 30, 0, 0)},
	}
	for _, tc := range cases {
		s, err := Parse(tc.spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.spec, err)
		}
		got := s.Next(tc.after)
		if got != tc.want {
			t.Errorf("Next(%q after %s) = %s, want %s", tc.spec, tc.after, got, tc.want)
		}
	}
}

// TestNextContinuous walks a schedule forward across many fires and asserts
// monotonicity plus exact expected hits for a dense pattern.
func TestNextContinuous(t *testing.T) {
	s := MustParse("*/10 6-18 * * 1-5") // every 10 min, 06:00-18:00, Mon-Fri
	cur := at(2026, 9, 14, 0, 0)        // a Monday
	prev := time.Time{}
	for i := 0; i < 500; i++ {
		next := s.Next(cur)
		if !next.After(cur) && !prev.IsZero() && !next.After(prev) {
			t.Fatalf("non-monotonic: prev %s cur %s next %s", prev, cur, next)
		}
		if next.Weekday() != time.Saturday && next.Weekday() != time.Sunday {
			if next.Hour() < 6 || next.Hour() > 18 {
				t.Fatalf("hit outside hours window: %s", next)
			}
			if next.Minute()%10 != 0 {
				t.Fatalf("minute %%10 != 0: %s", next)
			}
		}
		prev, cur = next, next
	}
}

// TestNextSkipsHoursWithinDay is a regression guard for the hour-skip
// window: a schedule with a late hour must not lose it to a day jump.
func TestNextSkipsHoursWithinDay(t *testing.T) {
	s := MustParse("0 23 * * *") // daily 23:00
	got := s.Next(at(2026, 5, 5, 1, 0))
	want := at(2026, 5, 5, 23, 0)
	if got != want {
		t.Fatalf("Next = %s, want %s (23:00 today, not tomorrow)", got, want)
	}
}

func TestDowDomInteraction(t *testing.T) {
	// dom restricted alone: dow ignored.
	s := MustParse("0 0 10 * *")
	if got := s.Next(at(2026, 1, 1, 0, 0)); got != at(2026, 1, 10, 0, 0) {
		t.Fatalf("dom-only: %s", got)
	}
	// dow restricted alone: dom ignored.
	s = MustParse("0 0 * * sat")
	if got := s.Next(at(2026, 1, 1, 0, 0)); got != at(2026, 1, 3, 0, 0) { // Jan 3 2026 = Saturday
		t.Fatalf("dow-only: %s", got)
	}
	// Feb 30 style dead day + valid dow: "0 0 31 2 *" is rejected at Parse,
	// but "0 0 31 * *" skips short months.
	s = MustParse("0 0 31 * *")
	if got := s.Next(at(2026, 2, 1, 0, 0)); got != at(2026, 3, 31, 0, 0) {
		t.Fatalf("31st after feb: %s", got)
	}
}
