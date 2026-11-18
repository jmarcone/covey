package daemon

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	// The reset times name their zone (Europe/Berlin); a sandbox image without
	// a zone database must still be able to read them.
	_ "time/tzdata"
)

// Reported utilisation.
//
// A limit is only worth as much as the measurement behind it, and there are
// three sources of decreasing quality (spec/18): what the ENGINE reports, what
// the platform ESTIMATES from what it booked, and what the provider REVEALS by
// rejecting a credential. This file is the first one.
//
// Claude Code answers `/usage` headless and without a model turn, with the
// provider's own figures: the share of the current rolling window and of the
// week. That beats anything inferred from our own bookkeeping — but it comes
// with two properties that have to be respected rather than wished away, and
// both are handled here:
//
//   - the answer is PROSE, not structured data, and the format will change
//     between engine versions. So parsing is fail-open: an answer that no
//     longer matches leaves the fleet running on the platform's own estimate,
//     it never blocks;
//   - the endpoint behind it has a RATE LIMIT of its own. Asking once per run
//     across a fleet would hit it, and three agents on one seat get the same
//     answer anyway — so the control plane asks centrally per credential and
//     caches (see orchestrator).

// Usage is what an engine reports about one credential.
type Usage struct {
	// WindowPercent/WeekPercent are shares of the plan limits, 0..100.
	// Negative means: not reported.
	WindowPercent float64 `json:"window_percent"`
	WeekPercent   float64 `json:"week_percent"`
	// WindowResets/WeekResets are the provider's own reset times as printed
	// ("Nov 20, 4:59pm (Europe/Berlin)"), if given.
	WindowResets string `json:"window_resets,omitempty"`
	WeekResets   string `json:"week_resets,omitempty"`
	// WindowResetsAt/WeekResetsAt are the same times read as instants, in the
	// zone the text names. Nil where the text gave none or could not be read —
	// a guessed reset would make a lagging reading look like a new week.
	WindowResetsAt *time.Time `json:"window_resets_at,omitempty"`
	WeekResetsAt   *time.Time `json:"week_resets_at,omitempty"`
	// Stale marks a figure the engine served from its own cache because the
	// provider's endpoint was rate limited. Reporting it as fresh would be
	// worse than having no figure — a limit would be compared against an hour
	// old number without anybody knowing.
	Stale bool `json:"stale"`
	// Fetched is when the figure was read from the provider: when we asked,
	// or for a stale figure the moment the engine says it last loaded it.
	// Zero for a stale figure whose age the text does not give — it is NOT
	// stamped with the time of the question, which would make an hour-old
	// figure look current.
	Fetched time.Time `json:"fetched"`
}

// Reported says whether anything usable came back.
func (u Usage) Reported() bool { return u.WindowPercent >= 0 || u.WeekPercent >= 0 }

// UsageReporter is the optional engine capability. An engine that cannot ask
// its provider simply does not implement it, and the platform falls back to its
// own estimate — which for at least one engine (Codex, spec/19) is the only
// source there is. That is precisely why the fallback exists: a capability only
// one engine implements is a special case, not a capability.
type UsageReporter interface {
	// Usage asks the provider about the credential currently in effect. The
	// credential arrives the same way it does for a run, so this runs where the
	// credential is: in the sandbox, via the daemon.
	Usage(ctx context.Context, env []string) (Usage, error)
}

// Parsing `/usage`. Deliberately tolerant: named percentages are picked out of
// the prose, everything else is ignored. What is not found stays unreported
// rather than becoming zero — zero would read as "plenty left", which is the
// wrong direction to be wrong in.
//
// The reset text is followed by its time zone in parentheses, captured on its
// own: "4:59pm" means nothing without it.
var (
	reSession = regexp.MustCompile(`(?i)current session:\s*([0-9.]+)%\s*used(?:\s*·\s*resets\s*([^\n(]*[^\s(])\s*(?:\(([^)\n]+)\))?)?`)
	reWeek    = regexp.MustCompile(`(?i)current week\s*\(all models\):\s*([0-9.]+)%\s*used(?:\s*·\s*resets\s*([^\n(]*[^\s(])\s*(?:\(([^)\n]+)\))?)?`)
	reStale   = regexp.MustCompile(`(?i)showing last-known usage(?:[^\n]*?fetched\s+(\d+)\s*(second|sec|minute|min|hour|hr|day)s?\s+ago)?`)
)

// staleUnits turns the age the staleness note gives into a duration.
var staleUnits = map[string]time.Duration{
	"second": time.Second, "sec": time.Second, "minute": time.Minute, "min": time.Minute,
	"hour": time.Hour, "hr": time.Hour, "day": 24 * time.Hour,
}

// ParseUsage reads the figures out of an engine's `/usage` answer.
//
// Exported for the tests, and because the format is exactly the brittle part:
// whoever changes it should be able to see it fail in a unit test rather than
// in the fleet.
func ParseUsage(text string) Usage { return ParseUsageAt(text, time.Now()) }

// ParseUsageAt is ParseUsage with the moment of the question given: the reset
// texts carry no year (the session's often no date either), and a stale answer
// states its age relative to now.
func ParseUsageAt(text string, now time.Time) Usage {
	u := Usage{WindowPercent: -1, WeekPercent: -1, Fetched: now}
	if m := reSession.FindStringSubmatch(text); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			u.WindowPercent = v
			u.WindowResets = resetsText(m[2], m[3])
			if at, ok := ParseResets(m[2], m[3], now); ok {
				u.WindowResetsAt = &at
			}
		}
	}
	if m := reWeek.FindStringSubmatch(text); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			u.WeekPercent = v
			u.WeekResets = resetsText(m[2], m[3])
			if at, ok := ParseResets(m[2], m[3], now); ok {
				u.WeekResetsAt = &at
			}
		}
	}
	if m := reStale.FindStringSubmatch(text); m != nil {
		u.Stale = true
		// The figure is as old as the engine says; without an age it has none
		// we could vouch for, and the moment of the question is not it.
		u.Fetched = time.Time{}
		if n, err := strconv.Atoi(m[1]); err == nil {
			u.Fetched = now.Add(-time.Duration(n) * staleUnits[strings.ToLower(m[2])])
		}
	}
	return u
}

// resetsText keeps the reset as printed, its zone included.
func resetsText(when, zone string) string {
	when, zone = strings.TrimSpace(when), strings.TrimSpace(zone)
	if when == "" || zone == "" {
		return when
	}
	return when + " (" + zone + ")"
}

// resetLayouts are the shapes the reset time has been seen in ("Aug 9 at
// 6:59am", "Nov 20, 4:59pm", "1:20pm"), once "at" and commas are gone.
var resetLayouts = []struct {
	layout string
	date   bool
}{
	{"Jan 2 3:04pm", true}, {"Jan 2 3pm", true}, {"Jan 2 15:04", true},
	{"3:04pm", false}, {"3pm", false}, {"15:04", false},
}

// ParseResets reads a reset time ("Nov 20, 4:59pm") in the zone the text
// names ("Europe/Berlin") as an instant.
//
// The text has no year: it is the one that puts the reset ahead of now (one
// that has just passed is allowed a day, for an answer read late). A time
// without a date is its next occurrence. Without a zone nothing is guessed —
// the instant would be off by hours, and a wrong reset time is worse than
// none: it decides whether a lower reading is a new week or a lagging one.
func ParseResets(when, zone string, now time.Time) (time.Time, bool) {
	when, zone = strings.TrimSpace(when), strings.TrimSpace(zone)
	if when == "" || zone == "" {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, false
	}
	norm := " " + strings.ToLower(strings.ReplaceAll(when, ",", " ")) + " "
	norm = strings.ReplaceAll(norm, " at ", " ")
	norm = strings.Join(strings.Fields(norm), " ")
	// Month names parse case-sensitively: "nov" has to become "Nov".
	if norm != "" && norm[0] >= 'a' && norm[0] <= 'z' {
		norm = strings.ToUpper(norm[:1]) + norm[1:]
	}
	local := now.In(loc)
	for _, l := range resetLayouts {
		t, err := time.ParseInLocation(l.layout, norm, loc)
		if err != nil {
			continue
		}
		if l.date {
			at := time.Date(local.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if at.Before(now.Add(-24 * time.Hour)) {
				at = time.Date(local.Year()+1, t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			}
			return at.UTC(), true
		}
		at := time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if at.Before(now) {
			at = time.Date(local.Year(), local.Month(), local.Day()+1, t.Hour(), t.Minute(), 0, 0, loc)
		}
		return at.UTC(), true
	}
	return time.Time{}, false
}
