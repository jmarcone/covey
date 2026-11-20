package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"covey/internal/daemon"
)

// covey's own meter poll (#153): a reading of the subscription meter that
// does not wait for a run.
//
// In-band readings (rate_limit_event) arrive only while a run streams. With
// the fleet paused or idle the meter goes unread, and the outside sessions
// that share a seat (#120, F3) stay unmetered: there is no step to split. The
// poll closes that gap with the cheapest call that makes Claude Code print a
// rate_limit_event — one Haiku turn — on each Claude Code subscription seat
// that is due (MeterPollDue). Only a seat an outside source is linked to is
// ever due: without outside work on it there is nothing to split, and the
// poll's own link would turn a seat that was all covey's into one held to
// watermarks.
//
// What it files:
//
//   - the event's windows as readings, source usage_cmd, in exactly the
//     shape of the in-band rows (one row per window, five-hour windows in
//     window_percent, the weekly ones in week_percent);
//   - the call's own tokens as outside usage of a source of its own,
//     MeterPollSourceFor(seat), linked to the seat it read. The poll moves the
//     meter it reads; booked like this it takes its share of the step like
//     any other outside session, and stays visible as what it is.
//
// The split itself (external.go) is not touched: it reads these rows as it
// reads every other reading and every other source.

// MeterPollSource is the prefix of the sources the poll books its calls
// under; one source per seat, since a source links to one meter.
const MeterPollSource = "meter poll"

// MeterPollHost is the host the poll's calls are booked from.
const MeterPollHost = "covey"

// MeterPollSourceFor is the source the poll of one seat books its calls
// under: "meter poll: <secret_key>#<slot>". A name past the source limit
// keeps the prefix and as much of the key as fits, then "~" and the first 10
// hex digits of sha256("<secret_key>#<slot>"): cut plainly, two long keys, or
// two slots of one, would share a source and so a link to one meter.
func MeterPollSourceFor(secretKey string, slot int) string {
	seat := secretKey + "#" + strconv.Itoa(slot)
	s := MeterPollSource + ": " + seat
	if utf8.RuneCountInString(s) <= externalMaxSource {
		return s
	}
	sum := sha256.Sum256([]byte(seat))
	tail := "~" + hex.EncodeToString(sum[:])[:10]
	head := []rune(MeterPollSource + ": " + secretKey)
	return string(head[:externalMaxSource-utf8.RuneCountInString(tail)]) + tail
}

// IsMeterPollSource says whether a source name is one of the poll's.
func IsMeterPollSource(source string) bool {
	return source == MeterPollSource || strings.HasPrefix(source, MeterPollSource+":")
}

// Why a seat is due for a poll. Either way an outside source is linked to it.
const (
	MeterPollOutside = "outside" // an outside source linked to it pushed recently
	MeterPollPaused  = "paused"  // the organisation's fleet is paused
)

// MeterPollCandidate is one seat due for a poll: the credential to read it
// through (the first place that holds the secret value) and why.
type MeterPollCandidate struct {
	OrgID      uuid.UUID
	Credential CredentialRef
	Reason     string
}

// meterPollSourceSQL is IsMeterPollSource in SQL, for the column named by
// %[1]s and the prefix in parameter $%[2]d: the source itself or the prefix
// and a colon. starts_with, not LIKE, so nothing in the prefix is a pattern.
const meterPollSourceSQL = `(%[1]s = $%[2]d OR starts_with(%[1]s, $%[2]d || ':'))`

// meterPollDue finds the due seats: $1 now, $2 the subscription kind, $3 and
// $4 now less fresh and less active, $5 MeterPollSource, $6 the Claude Code
// engine.
//
// A place is a credential in play: a subscription of a Claude Code runtime,
// not paused by hand and not in a cooldown at now. Another engine's
// subscription (Codex's ChatGPT plan, an educa seat) is a different secret
// the claude CLI must never be handed. The same secret value at several
// places is one seat and one meter, read through its first place (by the
// runtime's creation, its id, then ord), so the poll asks it once and always
// through the same place.
//
// A seat is due only when an outside source (one that is not the poll's) is
// linked to it: a seat no outside source names has no outside work to split,
// and the poll's own link would subject it to watermarks it was free of. Of
// those, a seat is due without a supported, fresh reading (in band or the
// poll's own; one entered by hand does not count) received within fresh, and
// when either such a source had a host push within active (outside), or its
// organisation's fleet is paused (paused): the fleet switch is off, or the
// organisation has agents and all of them are killed. The poll's own sources
// never make a seat due: otherwise each poll would call for the next.
var meterPollDue = `
WITH place AS (
	SELECT r.org_id, rc.secret_key, rc.secret_slot, rc.runtime_id, rc.ord, rc.kind,
		row_number() OVER (PARTITION BY r.org_id, rc.secret_key, rc.secret_slot
			ORDER BY r.created_at, r.id, rc.ord) AS n
	FROM runtime_credentials rc JOIN runtimes r ON r.id = rc.runtime_id
	WHERE rc.kind = $2 AND r.engine = $6 AND rc.paused_at IS NULL
	  AND (rc.cooldown_until IS NULL OR rc.cooldown_until <= $1)),
seat AS (
	SELECT p.org_id, p.secret_key, p.secret_slot, p.runtime_id, p.ord, p.kind,
		EXISTS (SELECT 1 FROM external_usage_meters m
			WHERE m.org_id = p.org_id AND m.secret_key = p.secret_key AND m.secret_slot = p.secret_slot
			  AND NOT ` + fmt.Sprintf(meterPollSourceSQL, "m.source", 5) + `) AS linked,
		EXISTS (SELECT 1 FROM external_usage_meters m
			JOIN external_usage_hosts h ON h.org_id = m.org_id AND h.source = m.source
			WHERE m.org_id = p.org_id AND m.secret_key = p.secret_key AND m.secret_slot = p.secret_slot
			  AND NOT ` + fmt.Sprintf(meterPollSourceSQL, "m.source", 5) + `
			  AND h.pushed_at > $4) AS outside,
		o.fleet_killed OR (EXISTS (SELECT 1 FROM agents a WHERE a.org_id = p.org_id)
			AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.org_id = p.org_id AND NOT a.killed)) AS paused
	FROM place p JOIN organizations o ON o.id = p.org_id
	WHERE p.n = 1
	  AND NOT EXISTS (SELECT 1 FROM usage_readings u
		WHERE u.org_id = p.org_id AND u.secret_key = p.secret_key AND u.secret_slot = p.secret_slot
		  AND u.source IN ('` + SourceInband + `', '` + SourceUsageCmd + `') AND u.supported AND NOT u.stale
		  AND u.received_at > $3))
SELECT org_id, secret_key, secret_slot, runtime_id, ord, kind, outside
FROM seat
WHERE linked AND (outside OR paused)
ORDER BY org_id, secret_key, secret_slot`

// MeterPollDue returns the Claude Code subscription seats to poll at now: an
// outside source (not one of the poll's own) linked to the seat, no
// supported, fresh reading (inband or usage_cmd) received within fresh, and
// either such a source's host pushed within active, or the organisation's
// fleet paused.
//
// One query across every organisation, each seat once (meterPollDue), in
// order of organisation, secret_key and slot. Every time is measured from
// now, never from the database's clock.
func (s *Store) MeterPollDue(ctx context.Context, now time.Time, fresh, active time.Duration) ([]MeterPollCandidate, error) {
	rows, err := s.pool.Query(ctx, meterPollDue, now, daemon.CredSubscription, now.Add(-fresh), now.Add(-active),
		MeterPollSource, daemon.EngineClaudeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeterPollCandidate
	for rows.Next() {
		var (
			c         MeterPollCandidate
			slot, ord int16
			outside   bool
		)
		if err := rows.Scan(&c.OrgID, &c.Credential.SecretKey, &slot, &c.Credential.RuntimeID, &ord,
			&c.Credential.Kind, &outside); err != nil {
			return nil, err
		}
		c.Credential.SecretSlot, c.Credential.Ord = int(slot), int(ord)
		c.Reason = MeterPollPaused
		if outside {
			c.Reason = MeterPollOutside
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddPolledReadings files a rate_limit_event the poll read as readings of
// cred, source usage_cmd — the in-band insert with the poll's source. at is
// the call's instant on the control plane's clock, the one its booking and
// watermark carry, and becomes the readings' fetched_at and received_at.
func (s *Store) AddPolledReadings(ctx context.Context, orgID uuid.UUID, cred CredentialRef, payload json.RawMessage, at time.Time) error {
	at = at.UTC()
	return s.addEventReadings(ctx, orgID, cred, payload, SourceUsageCmd, &at)
}

// AdvanceMeterPollMarks moves the watermark of every poll source's host up
// to now. covey books each poll before it files the reading, so its source
// has always reported through the present; without this, a poll source that
// went quiet would hold every later reading of its seat back from the split
// for as long as its host counts as active.
//
// The watermark only moves up (GREATEST), and a host without one gets its
// first here, dated now like a push would (the table's CHECK keeps the two
// together). pushed_at is left alone: it says when the poll last booked a
// call, not when covey vouched for it.
func (s *Store) AdvanceMeterPollMarks(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE external_usage_hosts SET
			reported_through = GREATEST(reported_through, $1),
			first_reported_at = COALESCE(first_reported_at, $1)
		WHERE `+fmt.Sprintf(meterPollSourceSQL, "source", 2)+`
		  AND (reported_through IS NULL OR reported_through < $1)`, now.UTC(), MeterPollSource)
	return err
}
