import type { UsageReading } from "../api";
import type { DisplayTz } from "../format";

/* The range of the Costs page: the quick ranges (?days=) and a custom one,
 * from and to to the minute (?from=&to=&tz=). A custom range is half-open,
 * [from, to), like the API's: a reading at `to` belongs to the next one.
 *
 * validRange is the API's rule (costRange in internal/httpapi/handlers.go),
 * and the codes are the API's too: a refusal from the server maps to the same
 * words as one from here. Keep both in step. */

const MIN = 60_000;
const DAY = 24 * 60 * MIN;

/** The longest custom range, as the API takes it. */
export const MAX_RANGE_DAYS = 31;

/** What the page shows when no ?tz= says: the reader's clock. */
export const DEFAULT_TZ: DisplayTz = "local";

/** Why a range is refused — also the API's `code` of a 400. */
export type RangeCode = "range_incomplete" | "range_time" | "range_order" | "range_span" | "range_future" | "range_bucket";
export const RANGE_CODES: readonly RangeCode[] = ["range_incomplete", "range_time", "range_order", "range_span", "range_future", "range_bucket"];
export const isRangeCode = (c: unknown): c is RangeCode => RANGE_CODES.includes(c as RangeCode);

/** The window the page asks the API for: the quick range's days, or a custom
 *  range's from and to (RFC 3339, minute precision) — then `days` is its span,
 *  rounded up, which the bucket limits read. */
export type CostWindow = { days: number; from?: string; to?: string };

export const isCustom = (w: CostWindow): boolean => w.from != null && w.to != null;

/** The query the cost endpoints take: ?days= or ?from=&to=. */
export function windowQuery(w: CostWindow): string {
  return isCustom(w) ? `from=${encodeURIComponent(w.from!)}&to=${encodeURIComponent(w.to!)}` : `days=${w.days}`;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** An instant as RFC 3339 at minute precision (floored), in UTC. */
export function isoMinute(ms: number): string {
  const d = new Date(Math.floor(ms / MIN) * MIN);
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:00Z`;
}

/** The next whole minute at or after ms: a window that should hold the
 *  current minute ends there. */
export const ceilMinute = (ms: number): number => Math.ceil(ms / MIN) * MIN;

/** An instant as the value of a datetime-local input, in the chosen zone. */
export function toInputValue(ms: number, tz: DisplayTz): string {
  if (!Number.isFinite(ms)) return "";
  const d = new Date(ms);
  return tz === "utc"
    ? `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
    : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The instant a datetime-local value names in the chosen zone; NaN for
 *  anything that is not a date and time to the minute (a day that does not
 *  exist, a local time skipped by a clock change). */
export function fromInputValue(v: string, tz: DisplayTz): number {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(v.trim());
  if (!m) return NaN;
  const [y, mo, d, h, mi] = m.slice(1).map(Number);
  const ms = tz === "utc" ? Date.UTC(y, mo - 1, d, h, mi) : new Date(y, mo - 1, d, h, mi).getTime();
  return toInputValue(ms, tz) === v.trim() ? ms : NaN;
}

/** Whether a range may be asked for; the code says why not. `now` is the
 *  clock (injected: no test depends on today). A to after now is allowed — the
 *  API cuts it to now. */
export function validRange(fromMs: number, toMs: number, nowMs: number): RangeCode | null {
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs)) return "range_time";
  if (toMs <= fromMs) return "range_order";
  if (toMs - fromMs > MAX_RANGE_DAYS * DAY) return "range_span";
  if (fromMs >= nowMs) return "range_future";
  return null;
}

/** The window of a valid custom range. */
export function customWindow(fromMs: number, toMs: number): CostWindow {
  return { days: Math.max(1, Math.ceil((toMs - fromMs) / DAY)), from: isoMinute(fromMs), to: isoMinute(toMs) };
}

/** What the URL says: the zone, and the custom range when from and to are
 *  there — with the code when they cannot be used (one without the other, not
 *  RFC 3339, back to front, too long, in the future). */
export type RangeParams = { tz: DisplayTz; from: number | null; to: number | null; error: RangeCode | null };

// What the page writes and the server reads the same way: RFC 3339 to the
// minute (seconds :00, no fraction), a zone, and a date that exists. Anything
// else — no seconds (Go's time.RFC3339 refuses it), seconds that customWindow
// would floor away, an impossible day — is range_time, so the window the page
// asks for is the one its address names.
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):00(?:Z|[+-]\d{2}:\d{2})$/;

function minuteExact(v: string): boolean {
  const m = RFC3339.exec(v);
  if (!m) return false;
  const [y, mo, d, h, mi] = m.slice(1).map(Number);
  if (h > 23 || mi > 59) return false;
  const day = new Date(Date.UTC(y, mo - 1, d));
  return day.getUTCFullYear() === y && day.getUTCMonth() === mo - 1 && day.getUTCDate() === d;
}

export function readRange(p: URLSearchParams, nowMs: number): RangeParams {
  const tzp = p.get("tz");
  const tz: DisplayTz = tzp === "utc" || tzp === "local" ? tzp : DEFAULT_TZ;
  const f = p.get("from"), t = p.get("to");
  if (f == null && t == null) return { tz, from: null, to: null, error: null };
  if (!f || !t) return { tz, from: null, to: null, error: "range_incomplete" };
  if (!minuteExact(f) || !minuteExact(t)) return { tz, from: null, to: null, error: "range_time" };
  const from = Date.parse(f), to = Date.parse(t);
  return { tz, from, to, error: validRange(from, to, nowMs) };
}

/** The range the cost pages open with: 30 days, the human's choice (2026-11-21, cost home proposal), so a link to
 *  C-level opens on a month, not on today. A range left out of the address is this one; both writers of the address
 *  (writeRange here, useCostWindow in costView) leave out exactly this one. */
export const DEFAULT_DAYS = 30;

/** The URL's range parameters for a window and a zone — the quick range
 *  (days, left out for the default range) or the custom one. Other parameters
 *  stay as they are. */
export function writeRange(prev: URLSearchParams, w: CostWindow, tz: DisplayTz): URLSearchParams {
  const n = new URLSearchParams(prev);
  for (const k of ["days", "from", "to", "tz"]) n.delete(k);
  if (isCustom(w)) {
    n.set("from", w.from!);
    n.set("to", w.to!);
  } else if (w.days !== DEFAULT_DAYS) n.set("days", String(w.days));
  // The zone is in the address whenever it was chosen, so a link reads in the
  // clock it was shared in.
  if (tz !== DEFAULT_TZ || isCustom(w)) n.set("tz", tz);
  return n;
}

/** The bucket a custom range starts at: a span shorter than a bucket is one
 *  bucket. Up to 3 hours by 10 minutes, up to 2 days by hours, longer by
 *  days (the limits of the buckets, outsideFigures). */
export function bucketForSpan(spanMs: number): string {
  if (spanMs <= 3 * 60 * MIN) return "10min";
  if (spanMs <= 2 * DAY) return "hour";
  return "day";
}

// --- The helpers ---------------------------------------------------------------

/** From the start of today in the zone to the end of this minute. */
export function todayRange(nowMs: number, tz: DisplayTz): [number, number] {
  const d = new Date(nowMs);
  const start = tz === "utc" ? Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())
    : new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  return [start, ceilMinute(nowMs)];
}

/** The last n minutes, up to the end of this minute. */
export function lastMinutesRange(nowMs: number, n: number): [number, number] {
  const to = ceilMinute(nowMs);
  return [to - n * MIN, to];
}

/** The longest "last N minutes" is the longest range. */
export const MAX_LAST_MINUTES = MAX_RANGE_DAYS * 24 * 60;

/** The meter's weekly window, as the readings name it. */
const WEEKLY = "seven_day";

/** The latest reset of the weekly meter that has passed: the reset time its
 *  readings name (resets_at) that lies at or before now. Null where no reading
 *  names one. */
export function lastResetAt(readings: readonly UsageReading[], nowMs: number): number | null {
  let best: number | null = null;
  for (const r of readings) {
    if (r.window_kind !== WEEKLY || !r.supported || !r.resets_at) continue;
    const t = Date.parse(r.resets_at);
    if (Number.isFinite(t) && t <= nowMs && (best == null || t > best)) best = t;
  }
  return best;
}

/** One reading to pick from. */
export type ReadingChoice = { id: string; at: number; percent: number; credential: string };

/** The weekly meter's readings, newest first, one per credential and instant:
 *  what "between two meter readings" picks from. At most `limit`. */
export function readingChoices(readings: readonly UsageReading[], limit = 60): ReadingChoice[] {
  const seen = new Set<string>();
  const out: ReadingChoice[] = [];
  for (const r of readings) {
    if (r.window_kind !== WEEKLY || !r.supported || r.week_percent == null) continue;
    const at = Date.parse(r.fetched_at);
    if (!Number.isFinite(at)) continue;
    const credential = r.secret_key ? `${r.secret_key}#${r.secret_slot ?? 0}` : `${r.runtime_id}#${r.credential_ord}`;
    const id = `${credential}@${r.fetched_at}`;
    if (seen.has(id)) continue;
    seen.add(id);
    out.push({ id, at, percent: r.week_percent, credential });
  }
  out.sort((a, b) => b.at - a.at);
  return out.slice(0, limit);
}

/** The range between two readings: the first is the window's baseline (it
 *  lies just before from), the second is in it (to lies just after it), so the
 *  window holds exactly the movement between them. The minute after each, as
 *  the range has minute precision. */
export function betweenReadings(a: number, b: number): [number, number] {
  const [first, second] = a <= b ? [a, b] : [b, a];
  return [Math.floor(first / MIN) * MIN + MIN, Math.floor(second / MIN) * MIN + MIN];
}
