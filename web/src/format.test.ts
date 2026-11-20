import { beforeEach, describe, expect, it } from "vitest";
import i18n from "./i18n";
import { exact, fmtBucket, fmtBytes, fmtClockUTC, fmtCount, fmtDelta, fmtNumber, fmtUSD, fmtUSDAxis, fmtUSDBelow, fmtUSDFixed, fmtUSDUnit, setDisplayTz } from "./format";

/* The reason stood on the cost side: 147952885 read cache tokens. Nobody reads
   that as a hundred forty-eight million — you count it through digit by digit.
   Next to it the same page showed `3.05 M`, because the cost view had its own
   formatter, and the total stood there as `2746 $`. Three spellings for the
   same kind of number. */

// The separators follow the language of the UI (see format.ts), so every test
// has to say which one it runs in. Before, the result hung on what the
// environment had last been set to.
beforeEach(async () => {
  await i18n.changeLanguage("de");
});

describe("fmtCount", () => {
  it("zeigt kleine Anzahlen ganz, mit Tausendertrennung", () => {
    expect(fmtCount(0)).toBe("0");
    expect(fmtCount(842)).toBe("842");
    expect(fmtCount(9999)).toBe("9.999");
  });

  it("kürzt ab, wo das Zählen anfängt", () => {
    expect(fmtCount(10_000)).toBe("10 k");
    expect(fmtCount(12_345)).toBe("12,3 k");
    expect(fmtCount(999_999)).toBe("1000 k");
    expect(fmtCount(1_500_000)).toBe("1,5 M");
    expect(fmtCount(147_952_885)).toBe("148 M");
  });

  // Above a million M stops helping: `2500 M` you count through digit by
  // digit again. The measured reason stood above an agent on covey.work —
  // `2.499.833.356` input tokens.
  it("hat eine Stufe für Milliarden, und die kennt die Sprache", async () => {
    const i18n = (await import("./i18n")).default;
    await i18n.changeLanguage("de");
    expect(fmtCount(2_499_833_356)).toBe("2,5 Mrd");
    expect(fmtCount(999_999_999)).toBe("1000 M");
    await i18n.changeLanguage("en");
    // `2,5 B` would read as bytes in German, `2.5 Mrd` as nothing in English —
    // the one character of this file that has to know the language.
    expect(fmtCount(2_499_833_356)).toBe("2.5 B");
    await i18n.changeLanguage("de");
  });

  // One decimal digit says something at 12,3 k and nothing at 148,0 M.
  it("hängt keine Null an, die nichts trägt", () => {
    expect(fmtCount(148_000_000)).toBe("148 M");
    expect(fmtCount(20_000)).toBe("20 k");
  });

  // The short number is for skimming; whoever checks a bill needs the digits —
  // and finds them in the tooltip next to it.
  it("hat eine Langfassung für den Tooltip", () => {
    expect(exact(147_952_885)).toBe("147.952.885");
    expect(exact(842)).toBe("842");
  });
});

describe("fmtUSD", () => {
  // A single run often costs fractions of a cent. Rounded to two digits a
  // whole page would read 0,00 $.
  // With a comma in German — once `12.30 $` stood here next to `2.746 $`, and
  // the same point meant two things in two lines.
  it("behält die Stellen, die ein Betrag noch trägt", () => {
    expect(fmtUSD(12.3)).toBe("12,30 $");
    expect(fmtUSD(0.4615)).toBe("0,46 $");
  });

  // Review C14: `0,0000 $` for a run that cost a fraction of a cent, and four
  // digits below a dollar that the figures do not carry.
  it("zeigt weniger als einen Cent als <0,01 $ und null als 0,00 $", () => {
    expect(fmtUSD(0.0042)).toBe("<0,01 $");
    expect(fmtUSD(0.00004)).toBe("<0,01 $");
    expect(fmtUSD(0)).toBe("0,00 $");
    expect(fmtUSD(-12.3)).toBe("-12,30 $");
  });

  it("trennt Tausender, wo sie anfangen zu helfen", () => {
    expect(fmtUSD(2746)).toBe("2.746 $");
    expect(fmtUSD(463.14)).toBe("463,14 $");
    expect(fmtUSD(1_234_567)).toBe("1.234.567 $");
  });
});

/* #83: a bound under a cent said "< <$0.01", a small negative ">-$0.01", a
   price under a cent "<$0.01" whatever it was, and a point price of 0.4615
   "$0.46" beside a total it does not multiply to. */
describe("fmtUSD edges", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("says a small negative amount is below a cent, not greater than minus one", () => {
    expect(fmtUSD(-0.004)).toBe("-<$0.01");
    expect(fmtUSD(-0.5)).toBe("-$0.50");
  });

  it("writes an upper bound without saying 'below' twice", () => {
    expect(fmtUSDBelow(1.846)).toBe("< $1.85");
    expect(fmtUSDBelow(0.004)).toBe("< $0.01");
    expect(fmtUSDBelow(0.004)).not.toContain("<$");
    expect(fmtUSDBelow(0)).toBe("< $0.00");
  });

  it("keeps the digits of a price that is multiplied on the same screen", () => {
    expect(fmtUSDFixed(1.846 / 4, 4)).toBe("$0.4615");
    expect(fmtUSDFixed(12.5, 4)).toBe("$12.5000");
    expect(fmtUSDFixed(0.00001, 4)).toBe("<$0.0001");
    expect(fmtUSDFixed(0, 4)).toBe("$0.0000");
    expect(fmtUSDFixed(0.00006, 4)).toBe("$0.0001");
    expect(fmtUSDFixed(0.00004, 4)).toBe("<$0.0001");
  });

  it("shows a unit price under a cent with the precision it needs", () => {
    expect(fmtUSDUnit(0.46)).toBe("$0.46");
    expect(fmtUSDUnit(0.0042)).toBe("$0.0042");
    expect(fmtUSDUnit(0.00004)).toBe("$0.000040");
    expect(fmtUSDUnit(0.0042)).not.toBe(fmtUSDUnit(0.0007));
    // an exact power of ten keeps two digits, a price that rounds up to a cent is a cent
    expect(fmtUSDUnit(0.001)).toBe("$0.0010");
    expect(fmtUSDUnit(0.0005)).toBe("$0.00050");
    expect(fmtUSDUnit(0.00995)).toBe("$0.01");
    expect(fmtUSDUnit(0.00999)).toBe("$0.01");
    expect(fmtUSDUnit(-0.00999)).toBe("-$0.01");
    // just under a power of ten the two digits come from the rounded price
    expect(fmtUSDUnit(0.000996)).toBe("$0.0010");
    expect(fmtUSDUnit(0.0000999)).toBe("$0.00010");
    expect(fmtUSDUnit(0.000994)).toBe("$0.00099");
    expect(fmtUSDUnit(1e-12)).toBe("<$0.00000001");
    expect(fmtUSDUnit(0)).toBe("$0.00");
  });

  it("follows the UI's separators and symbol", async () => {
    await i18n.changeLanguage("de");
    expect(fmtUSDFixed(0.4615, 4)).toBe("0,4615 $");
    expect(fmtUSDUnit(0.0042)).toBe("0,0042 $");
    expect(fmtUSDBelow(0.004)).toBe("< 0,01 $");
  });
});

/* #83 outside review: `compared until 08:00 PM UTC` in English, though the
   issue asked for HH:MM UTC. */
describe("fmtClockUTC", () => {
  const evening = new Date("2026-11-18T20:00:00Z");

  it("is a 24-hour clock in English", async () => {
    await i18n.changeLanguage("en");
    expect(fmtClockUTC(evening)).toBe("20:00 UTC");
    expect(fmtClockUTC("2026-11-18T08:05:00Z")).toBe("08:05 UTC");
  });

  it("reads midnight as 00:00, not 24:00", async () => {
    await i18n.changeLanguage("en");
    expect(fmtClockUTC(new Date("2026-11-19T00:00:00Z"))).toBe("00:00 UTC");
  });

  it("is the same in German", async () => {
    await i18n.changeLanguage("de");
    expect(fmtClockUTC(evening)).toBe("20:00 UTC");
  });
});

/* The other two formatters stood there already and stay as they are — held
   down here only so that consistency is checkable and not
   claimed. */
describe("die übrigen Formatierer", () => {
  it("fmtBytes bleibt bei der gröbsten Einheit, die noch beschreibt", () => {
    expect(fmtBytes(812)).toBe("812 B");
    // Below ten units with a decimal digit, above without — the same rule as
    // in fmtCount, only older.
    expect(fmtBytes(5_000)).toBe("4.9 kB");
    expect(fmtBytes(14_500)).toBe("14 kB");
  });

  it("fmtDelta ebenso", () => {
    expect(fmtDelta(42_000)).toBe("42 s");
    expect(fmtDelta(3 * 60_000)).toBe("3 min");
  });
});

/* The same numbers on the English UI. The reason stood in the head of an agent
   on covey.work: `2.847 $` — in German two thousand eight hundred, in English
   two dollars fifty-five. The same string, two numbers. */
describe("englische Schreibweise", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("dreht Tausender- und Dezimaltrenner um", () => {
    // and the symbol goes where English puts it
    expect(fmtUSD(2746)).toBe("$2,746");
    expect(fmtUSD(12.3)).toBe("$12.30");
    expect(fmtUSD(0.0042)).toBe("<$0.01");
    expect(exact(147_952_885)).toBe("147,952,885");
    expect(fmtCount(12_345)).toBe("12.3 k");
    expect(fmtCount(2_499_833_356)).toBe("2.5 B");
  });

  // The two hang together: `1.234,5` against `1,234.5` — whoever changes only
  // one produces a spelling that is right in neither language.
  it("mischt die beiden nie", () => {
    expect(fmtCount(9999)).toBe("9,999");
    expect(fmtUSD(1_234_567)).toBe("$1,234,567");
  });
});

/* The separators come out of Intl for the language of the UI. Until then every
   language but German got the English ones: `1,234.5` in French, where the
   reader expects `1 234,5`. French is asserted against Intl itself — its group
   separator is a narrow no-break space, and a space typed by hand into a test
   is the one that gets lost in an editor. */
describe("andere Sprachen", () => {
  it("französisch: Leerzeichen als Tausender, Komma als Dezimaltrenner", async () => {
    await i18n.changeLanguage("fr");
    expect(exact(1_234_567)).toBe(new Intl.NumberFormat("fr").format(1_234_567));
    expect(exact(1_234_567)).not.toContain(",");
    expect(fmtCount(12_345)).toBe("12,3 k");
    expect(fmtUSD(12.3)).toBe("12,30 $");
    expect(fmtUSD(2746)).toBe(`${new Intl.NumberFormat("fr").format(2746)} $`);
  });

  it("japanisch: wie englisch", async () => {
    await i18n.changeLanguage("ja");
    expect(exact(1_234_567)).toBe("1,234,567");
    expect(fmtCount(12_345)).toBe("12.3 k");
  });

  it("die Milliarden-Einheit bleibt, wie sie war", async () => {
    await i18n.changeLanguage("fr");
    expect(fmtCount(2_499_833_356)).toBe("2,5 B");
  });
});

describe("fmtNumber", () => {
  // Review C11: multiples and percents next to amounts used "." on every UI.
  it("schreibt feste Stellen mit den Trennern der Sprache", async () => {
    await i18n.changeLanguage("de");
    expect(fmtNumber(28.64, 1)).toBe("28,6");
    expect(fmtNumber(1234.5, 2)).toBe("1.234,50");
    expect(fmtNumber(-0.004, 2)).toBe("0,00");
    expect(fmtNumber(Infinity, 1)).toBe("∞");
    await i18n.changeLanguage("en");
    expect(fmtNumber(28.64, 1)).toBe("28.6");
  });
});

describe("fmtBucket", () => {
  // Review C14: day and week buckets are cut in UTC; a week is named by its
  // first day.
  it("nennt UTC bei Tag und Woche und die Woche nach ihrem ersten Tag", async () => {
    await i18n.changeLanguage("de");
    expect(fmtBucket("2026-11-17T00:00:00Z", "day")).toBe("17.11. UTC");
    expect(fmtBucket("2026-11-16T00:00:00Z", "week")).toBe("Woche ab 16.11. UTC");
    await i18n.changeLanguage("en");
    expect(fmtBucket("2026-11-16T00:00:00Z", "week")).toBe("week of 11/16 UTC");
  });

  // Review of #96: an hour bucket (and the dated "compared until") reads in a
  // 24-hour clock in every language, as fmtClockUTC does — not "08:00 PM".
  it("an hour bucket uses a 24-hour clock in English too", async () => {
    await i18n.changeLanguage("en");
    const s = fmtBucket("2026-11-17T20:00:00Z", "hour");
    expect(s).toContain("20:00");
    expect(s).not.toMatch(/PM|AM/);
    expect(s.endsWith(" UTC")).toBe(true);
    await i18n.changeLanguage("de");
    expect(fmtBucket("2026-11-17T20:00:00Z", "hour")).toBe("17.11., 20:00 UTC");
  });
});

describe("fmtBucket in the reader's time zone", () => {
  // The tests run in Asia/Tokyo (UTC+9, vite.config.ts).
  it("labels a bucket below a day on the reader's clock, naming the zone, and in UTC otherwise", async () => {
    await i18n.changeLanguage("de");
    expect(fmtBucket("2026-11-17T20:00:00Z", "hour", "utc")).toBe("17.11., 20:00 UTC");
    const local = fmtBucket("2026-11-17T20:00:00Z", "hour", "local");
    expect(local).toContain("18.11., 05:00");
    expect(local).not.toContain("UTC");
    expect(fmtBucket("2026-11-17T20:00:00Z", "10min", "local")).toContain("05:00");
  });

  it("keeps a day, a week and a month at UTC midnight whatever the zone", async () => {
    await i18n.changeLanguage("de");
    expect(fmtBucket("2026-11-17T00:00:00Z", "day", "local")).toBe("17.11. UTC");
    expect(fmtBucket("2026-11-16T00:00:00Z", "week", "local")).toBe("Woche ab 16.11. UTC");
  });

  it("follows the page's zone unless one is given", async () => {
    await i18n.changeLanguage("de");
    setDisplayTz("local");
    try {
      expect(fmtBucket("2026-11-17T20:00:00Z", "hour")).toContain("05:00");
    } finally {
      setDisplayTz("utc");
    }
    expect(fmtBucket("2026-11-17T20:00:00Z", "hour")).toBe("17.11., 20:00 UTC");
  });
});

describe("fmtUSDAxis", () => {
  it("keeps an axis tick as short as its step", async () => {
    await i18n.changeLanguage("en");
    expect(fmtUSDAxis(0)).toBe("$0");
    expect(fmtUSDAxis(0.25)).toBe("$0.25");
    expect(fmtUSDAxis(2.5)).toBe("$2.5");
    expect(fmtUSDAxis(12)).toBe("$12");
    expect(fmtUSDAxis(1500)).toBe("$1.5 k");
  });
});
