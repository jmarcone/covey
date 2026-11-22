// Small formatters that several views share. Largely without i18n: the units
// (s/min/h/d, k/M) are the same in both languages, the sentence around them the
// callers build over their own translation keys. The one exception stands at
// milliarde() and justifies itself there.

import i18n, { uiLocale } from "./i18n";

// fmtBytes brings a file size to the coarsest unit that still
// describes it — `812 B`, `14,2 kB`, `3,1 MB`. Used by the file browser of the
// workspace.
export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["kB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

// fmtUSD brings an amount to cents — `$2,746`, `$12.30`, `$0.42` — and an
// amount below a cent to `<$0.01`. Four digits below a dollar read as
// precision the figures do not have (`$0.4615` next to `$33.70`), and
// printed `$0.0000` for a run that cost a fraction of a cent: `<$0.01` says
// it was not nothing; a negative one is `-<$0.01`. Zero is `$0.00`. Used by
// the cost page and by the backlog (cost on the task card).
//
// From a thousand with thousands separators, because that is exactly where they
// start to help: `2746 $` you read digit by digit, `2.746 $` at one glance.
//
// The symbol stands where the UI's language puts it (`$12.30` in English,
// `12,30 $` in German), taken from Intl like the separators.
export function fmtUSD(v: number): string {
  const a = Math.abs(v);
  if (a > 0 && a < 0.01) return v > 0 ? `<${dollar(dezimal(0.01, 2))}` : `-<${dollar(dezimal(0.01, 2))}`;
  const num = a >= 1000 ? group(Math.round(a)) : dezimal(a, 2);
  return v < 0 ? `-${dollar(num)}` : dollar(num);
}

// fmtUSDBelow is an upper bound, `< $1.85`. fmtUSD already says "below" for
// an amount under a cent, and `< <$0.01` says it twice: a bound under a cent
// reads `< $0.01`, which is what it is.
export function fmtUSDBelow(v: number): string {
  return `< ${v > 0 && v < 0.01 ? dollar(dezimal(0.01, 2)) : fmtUSD(v)}`;
}

// fmtUSDFixed is an amount with a fixed number of decimal digits, `$0.4615` —
// for a price that stands beside the amount it was multiplied into: `4 points
// × $0.46` sits badly beside `$1.85`, `4 points × $0.4615` reads as what it
// is. Four digits bring the product within a cent for a few dozen points, not
// for hundreds; the `≈` beside it says so. Below half the last digit it says
// `<$0.0001`.
export function fmtUSDFixed(v: number, digits: number): string {
  const a = Math.abs(v);
  const least = 10 ** -digits;
  if (a > 0 && a < least / 2) return `${v < 0 ? "-" : ""}<${dollar(dezimal(least, digits))}`;
  const num = a >= 1000 ? group(Math.round(a)) : dezimal(a, digits);
  return v < 0 ? `-${dollar(num)}` : dollar(num);
}

// fmtUSDUnit is the price of one unit. From a cent up it is fmtUSD; below it
// two significant digits, `$0.0042`, `$0.000040`, because `<$0.01 / unit`
// for two different prices says nothing and a price is read to compare
// (Delta stands beside it). Down to a hundred-millionth of a dollar, then
// `<`. The digits come from the price rounded to two significant figures, so
// `$0.000996` is `$0.0010`, not `$0.00100`; a price that rounds up to a cent
// is a cent (`$0.01`).
export function fmtUSDUnit(v: number): string {
  const a = Math.abs(v);
  if (!(a > 0 && a < 0.01)) return fmtUSD(v);
  const r = Number(a.toPrecision(2));
  if (r >= 0.01) return fmtUSDFixed(v, 2);
  return fmtUSDFixed(v, Math.min(8, 1 - Math.floor(Math.log10(r))));
}

// dollar puts the symbol on the side the UI's language expects, with the
// space it puts between — one Intl.NumberFormat per locale, like trenner().
let cur: { loc: string; before: boolean; space: boolean } | undefined;
function dollar(num: string): string {
  const loc = uiLocale();
  if (cur?.loc !== loc) {
    const parts = new Intl.NumberFormat(loc, { style: "currency", currency: "USD", currencyDisplay: "narrowSymbol" }).formatToParts(1);
    const c = parts.findIndex((p) => p.type === "currency"), n = parts.findIndex((p) => p.type === "integer");
    cur = { loc, before: c >= 0 && c < n, space: parts.some((p) => p.type === "literal" && /\s/.test(p.value)) };
  }
  const sp = cur.space ? " " : "";
  return cur.before ? `$${sp}${num}` : `${num}${sp}$`;
}

// fmtNumber is a plain number with a fixed number of decimal digits, in the
// UI's spelling: `28,6`, `1.234,50` — for multiples, percents and shares
// that stand next to amounts and must not switch to a point.
export function fmtNumber(v: number, digits: number): string {
  if (!Number.isFinite(v)) return v > 0 ? "∞" : v < 0 ? "-∞" : "–";
  const neg = v < 0 && Math.abs(v) >= 0.5 * 10 ** -digits;
  const [int, frac] = Math.abs(v).toFixed(digits).split(".");
  return `${neg ? "-" : ""}${group(Number(int))}${frac ? komma() + frac : ""}`;
}

// dezimal: a fixed number of decimal digits, with the separator of the UI.
// Without it the German page would show `12.30 $` next to `2.847 $`, and the
// same point would mean two things in one line.
function dezimal(v: number, stellen: number): string {
  return v.toFixed(stellen).replace(".", komma());
}

// fmtCount brings a count to what a person takes in while skimming — `842`,
// `12,3 k`, `148 M`.
//
// The reason was a number from the cost view: 147952885 read cache tokens.
// Nobody reads that as a hundred forty-eight million, you count it through
// digit by digit. Meant for tokens, blocks, files — everything of which an
// instance has six-figure amounts.
//
// The exact number is not lost in the process: whoever needs it (checking a
// bill) finds it in the `title` next to it — that is what exact() is for.
export function fmtCount(n: number): string {
  const v = Math.round(n);
  if (v < 10_000) return group(v);
  if (v < 1_000_000) return `${trim(v / 1000)} k`;
  if (v < 1_000_000_000) return `${trim(v / 1_000_000)} M`;
  return `${trim(v / 1_000_000_000)} ${milliarde()}`;
}

// milliarde: the one character of this file that has to know the language.
//
// k and M are named the same in both languages — the billion is not: `Mrd`
// against `B`, and a German `2,5 B` would read as bytes. Hence the exception
// from the rule above, and only here. Without this step an agent with a grown
// cache would stand there with `2500 M`, and that you count through digit by
// digit again — exactly what fmtCount set out against.
function milliarde(): string {
  return deutsch() ? "Mrd" : "B";
}

// exact is the long form for the tooltip next to a shortened number.
export function exact(n: number): string {
  return group(Math.round(n));
}

// group sets thousands separators — in the spelling of the UI.
//
// The point stood here hard-coded once, with the same reasoning as above: a
// number that looks different depending on the browser language makes
// screenshots incomparable. For units that argument holds, for separators it
// does not: `1.234` is one thousand two hundred thirty-four in German and
// something over one in English. On the English UI `2.847 $` therefore stood for
// an amount of two thousand eight hundred — read as two dollars fifty-five.
function group(v: number): string {
  const neg = v < 0;
  const digits = Math.abs(v).toString();
  const sep = tausender();
  let out = "";
  for (let i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 === 0) out += sep;
    out += digits[i];
  }
  return neg ? `-${out}` : out;
}

// trim shows one decimal digit as long as it says something: `12,3 k`, but
// `148 M` and not `148,0 M`.
function trim(v: number): string {
  const one = v.toFixed(1);
  const rounded = one.endsWith(".0") ? one.slice(0, -2) : one;
  return rounded.replace(".", komma());
}

// The two separators, and the one place where they are decided. They hang
// together: whoever sets the point as the thousands separator has to set the
// comma as the decimal separator, otherwise `1.234,5` against `1,234.5` makes a
// mishmash that is right in neither of the two languages. Both come out of one
// Intl.NumberFormat of the UI locale — the de/else switch that stood here gave
// every language but German the English `1,234.5`, and French `1 234,5`
// (a narrow no-break space) was not reachable.
// They are kept per locale: building an Intl.NumberFormat for every number a
// table formats is the expensive part, and the separators only change when the
// UI locale does.
let sep: { loc: string; tausend: string; komma: string } | undefined;
function trenner(): { tausend: string; komma: string } {
  const loc = uiLocale();
  if (sep?.loc !== loc) {
    const parts = new Intl.NumberFormat(loc).formatToParts(1234567.8);
    sep = {
      loc,
      tausend: parts.find((p) => p.type === "group")?.value ?? ",",
      komma: parts.find((p) => p.type === "decimal")?.value ?? ".",
    };
  }
  return sep;
}

function tausender(): string {
  return trenner().tausend;
}

function komma(): string {
  return trenner().komma;
}

// deutsch is what milliarde() asks, and nothing else: the unit stays a unit
// and does not follow Intl.
function deutsch(): boolean {
  return i18n.language?.startsWith("de") ?? true;
}

// fmtDelta brings a span of time in milliseconds to the coarsest unit that
// still describes it — `42 s`, `3 min`, `2 h 15 min`, `4 d`. Used by the
// Heartbeat (next/last run) and by the Activity-Feed (duration of a sub-run).
export function fmtDelta(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d} d ${h % 24} h` : `${d} d`;
}

// fmtUSDAxis is fmtUSD for an axis tick: as short as the round steps allow —
// `0 $`, `0,25 $`, `12 $`, `1,5 k $`. An axis with `12,00 $` next to
// `0,2500 $` reads as two scales.
//
// With the axis' step, every tick carries the step's precision: at a step of
// 0,0025 $ the ticks read `0,0025 $`, `0,0050 $`, not `0,003 $`, and at a top
// of 0,0003 $ not `0 $` four times.
export function fmtUSDAxis(v: number, step?: number): string {
  if (v === 0) return dollar("0");
  if (step != null && step > 0) {
    if (Math.abs(v) >= 1000) return dollar(`${fmtNumber(v / 1000, stepDigits(step / 1000))} k`);
    return dollar(fmtNumber(v, stepDigits(step)));
  }
  if (Math.abs(v) >= 1000) return dollar(`${trim(v / 1000)} k`);
  const digits = Math.abs(v) >= 10 ? 0 : Math.abs(v) >= 1 ? 1 : Math.abs(v) >= 0.1 ? 2 : 3;
  let s = v.toFixed(digits);
  if (s.includes(".")) s = s.replace(/0+$/, "").replace(/\.$/, "");
  return dollar(s.replace(".", komma()));
}

// fmtCountAxis is fmtCount for an axis tick with the axis' step: in the unit
// fmtCount would pick for the tick, with as many digits as the step carries
// there — `750 k`, `1,25 M` at a step of 250 k, where fmtCount rounds to
// `1,3 M`.
export function fmtCountAxis(v: number, step: number): string {
  const a = Math.abs(v);
  if (v === 0 || !(step > 0)) return fmtCount(v);
  if (a >= 1_000_000_000) return `${fmtNumber(v / 1e9, stepDigits(step / 1e9))} ${milliarde()}`;
  if (a >= 1_000_000) return `${fmtNumber(v / 1e6, stepDigits(step / 1e6))} M`;
  if (a >= 10_000) return `${fmtNumber(v / 1e3, stepDigits(step / 1e3))} k`;
  return fmtNumber(v, stepDigits(step));
}

// stepDigits: the decimal digits a step needs to be written exactly — 0 for
// 250, 2 for 0,25, 4 for 0,0025.
function stepDigits(step: number): number {
  for (let d = 0; d < 10; d++) {
    const x = step * 10 ** d;
    if (Math.abs(x - Math.round(x)) < 1e-6 * Math.max(1, Math.abs(x))) return d;
  }
  return 10;
}

// The time zone the cost page shows its times in. UTC unless the page says
// otherwise (the Costs page sets it from its ?tz=, see costRange.ts): one
// switch for every label on the page, so a tile and the axis never show two
// clocks.
export type DisplayTz = "utc" | "local";
let displayTz: DisplayTz = "utc";
export function setDisplayTz(tz: DisplayTz) {
  displayTz = tz;
}
export const getDisplayTz = (): DisplayTz => displayTz;

// fmtBucket labels a cost bucket in the UI's language. Buckets are cut in UTC
// (the server truncates in UTC): a day, a week and a month are UTC's and are
// shown so and say so — a local clock would shift every bar by the offset, and
// a UTC day is not the reader's day. The buckets below a day follow the
// display time zone (UTC by default, the reader's clock when the page asks
// for it, with the zone named). A week is named by its first day (Monday,
// UTC): "week of".
export function fmtBucket(iso: string, bucket: string, tz: DisplayTz = displayTz): string {
  const d = new Date(iso);
  const lang = uiLocale();
  switch (bucket) {
    case "10min":
    case "hour":
    case "10hour":
      return tz === "local"
        ? d.toLocaleString(lang, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZoneName: "short" })
        : `${d.toLocaleString(lang, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" })} UTC`;
    case "month":
      return d.toLocaleDateString(lang, { month: "short", year: "numeric", timeZone: "UTC" });
    case "week":
      return i18n.t("costs.weekOf", { date: `${d.toLocaleDateString(lang, { day: "2-digit", month: "2-digit", timeZone: "UTC" })} UTC` });
    default:
      return `${d.toLocaleDateString(lang, { day: "2-digit", month: "2-digit", timeZone: "UTC" })} UTC`;
  }
}

// fmtClock is an instant as HH:MM on the page's clock (the display time
// zone), with the zone named.
export function fmtClock(iso: string, tz: DisplayTz = displayTz): string {
  return tz === "local"
    ? new Date(iso).toLocaleTimeString(uiLocale(), { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZoneName: "short" })
    : fmtClockUTC(iso);
}

// fmtClockUTC is an instant as HH:MM UTC — how far Anthropic's bill is
// complete. A 24-hour clock in every language: `20:00 UTC`, not `08:00 PM UTC`.
export function fmtClockUTC(iso: string | Date): string {
  const d = typeof iso === "string" ? new Date(iso) : iso;
  return `${d.toLocaleTimeString(uiLocale(), { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" })} UTC`;
}
