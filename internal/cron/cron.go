// Package cron parses PM2-shaped cron_restart expressions (compat.md row
// 20) and computes fire times.
//
// Grammar: standard vixie-crontab five fields
//
//	minute  hour  day-of-month  month  day-of-week
//
// with `*`, lists (`a,b,c`), ranges (`a-b`), steps (`*/n`, `a-b/n`),
// month names (jan..dec) and weekday names (sun..sat, 0 and 7 = sunday),
// plus the @aliases @yearly/@annually, @monthly, @weekly, @daily/@midnight,
// @hourly. Leading/trailing whitespace is ignored.
//
// Semantics (vixie cron): when BOTH day-of-month and day-of-week are
// restricted (neither is `*`), a day matches if EITHER field matches; when
// one of them is `*`, it must match alone. All other fields intersect.
//
// Evaluation is UTC, minute-resolution — cron_restart fires when the wall
// clock enters a matching minute; second/sub-minute granularity does not
// exist in cron and the daemon polls at 1s.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron_restart expression.
type Schedule struct {
	spec string
	min  uint64 // bitsets: bit i set = value i allowed (field-local)
	hour uint64
	dom  uint64
	mon  uint64
	dow  uint64
	// domRestricted/dowRestricted record whether the field is not `*`
	// (the vixie dom/dow OR rule needs both flags).
	domRestricted bool
	dowRestricted bool
}

// Field-local "unrestricted" masks (every value of the field allowed).
// dom/mon are 1-based; min 0..59, hour 0..23, dow 0..6. (vars: bit() is
// not a constant expression.)
var (
	allHour = bit(0, 24)
	allDom  = bit(1, 32)
	allMon  = bit(1, 13)
	allDow  = bit(0, 7)
)

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// bit returns a bitset with bits [lo, hi) set (field-local 0-based).
func bit(lo, hi int) uint64 {
	if lo < 0 {
		lo = 0
	}
	if hi > 64 {
		hi = 64
	}
	if hi <= lo {
		return 0
	}
	return (^uint64(0) >> (64 - uint(hi-lo))) << uint(lo)
}

// aliases expand the @shorthands to per-field bitsets
// (minute, hour, dom, mon, dow) exactly as the equivalent vixie line:
//
//	@yearly = 0 0 1 1 *   @monthly = 0 0 1 * *   @weekly = 0 0 * * 0
//	@daily = 0 0 * * *    @hourly = 0 * * * *
var aliases = map[string][5]uint64{
	"@yearly":   {bit(0, 1), bit(0, 1), bit(1, 2), bit(1, 2), allDow},
	"@annually": {bit(0, 1), bit(0, 1), bit(1, 2), bit(1, 2), allDow},
	"@monthly":  {bit(0, 1), bit(0, 1), bit(1, 2), allMon, allDow},
	"@weekly":   {bit(0, 1), bit(0, 1), allDom, allMon, bit(0, 1)},
	"@daily":    {bit(0, 1), bit(0, 1), allDom, allMon, allDow},
	"@midnight": {bit(0, 1), bit(0, 1), allDom, allMon, allDow},
	"@hourly":   {bit(0, 1), allHour, allDom, allMon, allDow},
}

// Parse validates spec and returns its Schedule.
func Parse(spec string) (Schedule, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return Schedule{}, fmt.Errorf("cron: empty expression")
	}
	if bits, ok := aliases[strings.ToLower(s)]; ok {
		return Schedule{
			spec: s, min: bits[0], hour: bits[1], dom: bits[2],
			mon: bits[3], dow: bits[4],
			domRestricted: bits[2] != allDom, dowRestricted: bits[4] != allDow,
		}, nil
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron: %q: want 5 fields (min hour dom mon dow), got %d", s, len(fields))
	}
	var out Schedule
	out.spec = s
	var err error
	if out.min, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: minute: %w", err)
	}
	if out.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: hour: %w", err)
	}
	if out.dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: day-of-month: %w", err)
	}
	out.domRestricted = fields[2] != "*"
	if out.mon, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("cron: month: %w", err)
	}
	// dow: 0..7 with a literal 7 folding onto 0 (both mean sunday).
	var rawDow uint64
	if rawDow, err = parseField(fields[4], 0, 7, dowNames); err != nil {
		return Schedule{}, fmt.Errorf("cron: day-of-week: %w", err)
	}
	out.dow = rawDow&bit(0, 7) | (rawDow>>7)&1
	out.dowRestricted = fields[4] != "*"
	if out.min == 0 || out.hour == 0 || (out.dom == 0 && out.dow == 0) || out.mon == 0 {
		return Schedule{}, fmt.Errorf("cron: %q matches no time", s)
	}
	if err := out.checkFeasible(); err != nil {
		return Schedule{}, err
	}
	return out, nil
}

// maxDays is the longest a month can be (Feb 29 covers leap years).
var maxDays = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// checkFeasible rejects schedules that can never fire: day-of-month
// values beyond the length of every allowed month (Feb 31, Apr 31, ...).
// Only applies when dow is unrestricted — with both dom and dow
// restricted the vixie OR rule lets the dow side fire regardless.
func (s Schedule) checkFeasible() error {
	if s.dom == 0 || s.dowRestricted {
		return nil
	}
	for m := 1; m <= 12; m++ {
		if s.mon&(uint64(1)<<uint(m)) == 0 {
			continue
		}
		if s.dom&bit(1, maxDays[m]+1) != 0 {
			return nil // some real day inside month m matches
		}
	}
	return fmt.Errorf("cron: %q matches no time (day-of-month exceeds every allowed month's length)", s.spec)
}

// MustParse panics on invalid spec (tests and embedded defaults only).
func MustParse(spec string) Schedule {
	s, err := Parse(spec)
	if err != nil {
		panic(err)
	}
	return s
}

// Spec returns the original expression.
func (s Schedule) Spec() string { return s.spec }

// parseField parses one cron field into a bitset with bit v set for each
// allowed value v. Values are range-checked; names map through the
// (possibly nil) table. Steps apply to `*` and ranges alike.
func parseField(f string, lo, hi int, names map[string]int) (uint64, error) {
	var out uint64
	for _, part := range strings.Split(f, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("%q: empty list element", f)
		}
		base, stepStr := part, ""
		if i := strings.IndexByte(part, '/'); i >= 0 {
			base, stepStr = part[:i], part[i+1:]
		}
		step := 1
		if stepStr != "" {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("%q: bad step %q", f, stepStr)
			}
			step = n
		}
		var a, b int
		switch {
		case base == "*":
			a, b = lo, hi
		case strings.Contains(base, "-"):
			parts := strings.SplitN(base, "-", 2)
			var err error
			if a, err = parseValue(parts[0], names); err != nil {
				return 0, fmt.Errorf("%q: %w", f, err)
			}
			if b, err = parseValue(parts[1], names); err != nil {
				return 0, fmt.Errorf("%q: %w", f, err)
			}
		default:
			v, err := parseValue(base, names)
			if err != nil {
				return 0, fmt.Errorf("%q: %w", f, err)
			}
			a = v
			if stepStr != "" {
				b = hi // vixie: `n/step` = start at n, step to the field max
			} else {
				b = a // single value
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q: value out of range [%d,%d]", f, lo, hi)
		}
		for v := a; v <= b; v += step {
			out |= uint64(1) << uint(v)
		}
	}
	return out, nil
}

// parseValue resolves one literal: a number or a (lowercased) name.
func parseValue(tok string, names map[string]int) (int, error) {
	tok = strings.ToLower(strings.TrimSpace(tok))
	if n, err := strconv.Atoi(tok); err == nil {
		return n, nil
	}
	if names != nil {
		if v, ok := names[tok]; ok {
			return v, nil
		}
	}
	return 0, fmt.Errorf("bad value %q", tok)
}

// Next returns the first minute strictly after `after` whose wall clock
// matches the schedule (UTC). The standard bounded forward search: whole
// hours and days are skipped whenever the coarser field cannot match. A
// matching minute always exists within one leap cycle for schedules Parse
// accepts, so the loop is finite by construction.
func (s Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 1) // full leap cycle + slack
	for t.Before(limit) {
		// month bits are field-local: bit v = month v (1..12)
		if s.mon&(uint64(1)<<uint(t.Month())) == 0 {
			t = nextMonth(t)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.hour&(uint64(1)<<uint(t.Hour())) == 0 {
			// Next hour boundary — a later hour TODAY may still match, so
			// jumping straight to midnight would silently skip it.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if s.min&(uint64(1)<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	// Unreachable for schedules Parse accepts (no-match is rejected there).
	return time.Time{}
}

// dayMatches applies the vixie dom/dow rule: both restricted -> OR;
// otherwise the single restricted field decides.
func (s Schedule) dayMatches(t time.Time) bool {
	domBit := s.dom&(uint64(1)<<uint(t.Day())) != 0
	dowBit := s.dow&(uint64(1)<<uint(int(t.Weekday()))) != 0
	switch {
	case s.domRestricted && s.dowRestricted:
		return domBit || dowBit
	case s.domRestricted:
		return domBit
	case s.dowRestricted:
		return dowBit
	default:
		return true
	}
}

// nextMonth advances t to the first instant of the following month.
func nextMonth(t time.Time) time.Time {
	y, m := t.Year(), t.Month()
	if m == time.December {
		return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
}
