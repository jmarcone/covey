package observability

import (
	"fmt"
	"time"
)

// The cost answers cover a window [since, until). The relative ranges (?days=)
// have no end: a zero until is a window that runs to now, and every reader
// then reads what it always read. A window asked for as from/to ends at until,
// exclusive — a row stamped at until belongs to the next window — and each
// reader stops there, so a figure is what covey would have shown at that
// instant, not a longer window cut afterwards.

// untilArg is until as a query argument: its instant, or NULL for an open window.
func untilArg(until time.Time) any {
	if until.IsZero() {
		return nil
	}
	return until.UTC()
}

// beforeSQL is the SQL condition "col < $n", true whatever col is while the
// window is open ($n NULL).
func beforeSQL(col string, n int) string {
	return fmt.Sprintf("($%d::timestamptz IS NULL OR %s < $%d)", n, col, n)
}

// untilPtr is until for a JSON answer: absent while the window is open.
func untilPtr(until time.Time) *time.Time {
	if until.IsZero() {
		return nil
	}
	u := until.UTC()
	return &u
}

// windowEnd is where a window ends: until, or now for an open one.
func windowEnd(until, now time.Time) time.Time {
	if until.IsZero() {
		return now
	}
	return until
}
