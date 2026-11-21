import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router";
import { HOUR_MAX_DAYS, RANGES, TEN_HOUR_MAX_DAYS, TEN_MIN_MAX_DAYS } from "./outsideFigures";

/* The cost pages keep their view in the address (cost home proposal, PR 2): a
 * link opens exactly what its sender saw, the Back button returns to it, and a
 * saved view (later) is just a named link. Each setting has a default that is
 * left out of the address, a parser that refuses what it does not know (a hand
 * edited or outdated link falls back to the default, never to an error), and is
 * written with `replace`, so a filter change does not add a history step. */

type Updater<T> = T | ((prev: T) => T);

/** One setting held in the address under `key`. */
export function useParamState<T>(key: string, def: T, parse: (raw: string) => T | null, show: (v: T) => string): [T, (v: Updater<T>) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get(key);
  // Parsed once per address value, so a set or object keeps its identity between renders (effects and memos depend on it).
  const value = useMemo(() => (raw == null ? null : parse(raw)) ?? def, [raw, parse, def]);
  const set = useCallback((next: Updater<T>) => setParams((prev) => {
    const p = new URLSearchParams(prev);
    const cur = (() => { const r = p.get(key); const v = r == null ? null : parse(r); return v ?? def; })();
    const v = typeof next === "function" ? (next as (prev: T) => T)(cur) : next;
    const s = show(v);
    if (s === show(def) || s === "") p.delete(key);
    else p.set(key, s);
    return p;
  }, { replace: true }), [key, def, parse, show, setParams]);
  return [value, set];
}

/** Codecs for the common shapes. */
export const asBool = { parse: (r: string) => (r === "1" ? true : r === "0" ? false : null), show: (v: boolean) => (v ? "1" : "0") };
export function asEnum<T extends string>(values: readonly T[]) {
  return { parse: (r: string) => (values.includes(r as T) ? (r as T) : null), show: (v: T) => v };
}
/** A set of ids or names, comma-separated and sorted, so the same selection is always the same link. */
export const asSet = {
  parse: (r: string): ReadonlySet<string> => new Set(r.split(",").map((x) => x.trim()).filter(Boolean)),
  show: (v: ReadonlySet<string>) => [...v].sort().join(","),
};

/** The days the page offers, and the bucket each opens with. 24 hours until the 30-day default the human chose
 *  (2026-11-21) lands in its own change, with the page's tests moved to explicit ranges. */
export const DEFAULT_DAYS = 1;
export const BUCKETS = ["10min", "hour", "10hour", "day", "week", "month"] as const;
export const defaultBucket = (days: number) => RANGES.find((r) => r.v === days)?.bucket ?? "day";

/** Whether a bucket fits a range: 10 minutes up to 2 days, hourly up to 7, 10 hours up to 30. */
export function bucketFits(days: number, bucket: string): boolean {
  if (!(BUCKETS as readonly string[]).includes(bucket)) return false;
  if (bucket === "10min") return days <= TEN_MIN_MAX_DAYS;
  if (bucket === "hour") return days <= HOUR_MAX_DAYS;
  if (bucket === "10hour") return days <= TEN_HOUR_MAX_DAYS;
  return true;
}

/** The range and bucket of the cost pages, shared by /costs and /costs/outside (same keys, so a link between them
 *  keeps both). A bucket that does not fit the range falls back to the range's own; a new range keeps the chosen
 *  bucket where it still fits (it used to reset it). */
export function useCostWindow(): { days: number; bucket: string; setDays: (d: number) => void; setBucket: (b: string) => void } {
  const [params, setParams] = useSearchParams();
  const d = Number(params.get("days"));
  const days = RANGES.some((r) => r.v === d) ? d : DEFAULT_DAYS;
  const want = params.get("bucket");
  const bucket = want != null && bucketFits(days, want) ? want : defaultBucket(days);
  const write = useCallback((nd: number, nb: string) => setParams((prev) => {
    const p = new URLSearchParams(prev);
    if (nd === DEFAULT_DAYS) p.delete("days"); else p.set("days", String(nd));
    if (nb === defaultBucket(nd)) p.delete("bucket"); else p.set("bucket", nb);
    return p;
  }, { replace: true }), [setParams]);
  const setDays = useCallback((nd: number) => write(nd, bucketFits(nd, bucket) ? bucket : defaultBucket(nd)), [write, bucket]);
  const setBucket = useCallback((nb: string) => write(days, bucketFits(days, nb) ? nb : defaultBucket(days)), [write, days]);
  return { days, bucket, setDays, setBucket };
}

/** The current address's query, for a link to another cost page that should open with the same view. */
export function useCostQuery(drop: readonly string[] = []): string {
  const [params] = useSearchParams();
  const p = new URLSearchParams(params);
  for (const k of drop) p.delete(k);
  const s = p.toString();
  return s ? `?${s}` : "";
}
