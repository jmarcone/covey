import { describe, expect, it } from "vitest";

/* Every date, time and number separator follows the language of the UI —
   `uiLocale()` in i18n.ts — and nothing else (#56, part of #21).

   Two spellings let it slip, and both compile: a `"de-DE"`/`"en-US"` literal
   (the de/else switch gave every language but German the English dates), and a
   `toLocale*String()` or `Intl.*Format` without a locale, which takes the
   language of the BROWSER — German dates next to an English screen. A review
   finds them once; this finds them every time, and names the line. */

type Fund = { file: string; line: number; was: string };

// A literal, or a call that names no locale (empty, `[]` or `undefined`).
const MUSTER: { was: string; re: RegExp }[] = [
  { was: 'locale literal "de-DE"/"en-US" — use uiLocale()', re: /["'`](?:de-DE|en-US)["'`]/g },
  {
    was: "toLocale*String without a locale — use uiLocale()",
    re: /\.toLocale(?:Date|Time)?String\(\s*(?:\)|\[\s*\]|undefined\b)/g,
  },
  {
    was: "Intl.*Format without a locale — use uiLocale()",
    re: /Intl\.(?:DateTimeFormat|NumberFormat|RelativeTimeFormat|ListFormat|PluralRules|Collator)\(\s*(?:\)|\[\s*\]|undefined\b)/g,
  },
];

function finde(file: string, source: string): Fund[] {
  const funde: Fund[] = [];
  for (const { was, re } of MUSTER) {
    for (const m of source.matchAll(re)) {
      const line = source.slice(0, m.index).split("\n").length;
      funde.push({ file, line, was });
    }
  }
  return funde.sort((a, b) => a.line - b.line);
}

// One entry per file that may keep a hit, each with the reason. Tests and
// locales/ are not scanned at all, so they are not listed.
const ERLAUBT: Record<string, string> = {};

const quellen = import.meta.glob(["/src/**/*.{ts,tsx}", "!/src/**/*.test.*", "!/src/locales/**"], {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

describe("Locale in Datum, Uhrzeit und Zahl", () => {
  it("web/src nennt überall die Sprache der Oberfläche", () => {
    const funde = Object.entries(quellen).flatMap(([pfad, src]) => {
      const file = pfad.slice(1);
      return file in ERLAUBT ? [] : finde(file, src);
    });
    expect(funde.map((f) => `${f.file}:${f.line}  ${f.was}`)).toEqual([]);
  });

  it("jede Ausnahme ist noch nötig", () => {
    for (const file of Object.keys(ERLAUBT)) {
      const src = quellen["/" + file];
      expect(src, `${file} gibt es nicht mehr`).toBeDefined();
      expect(finde(file, src).length, `${file} braucht keine Ausnahme mehr`).toBeGreaterThan(0);
    }
  });

  // The guard has to bite, or it guards nothing.
  it("schlägt bei einem locale-losen Aufruf an und nennt die Zeile", () => {
    const src = "const a = 1;\nconst b = new Date().toLocaleString();\n";
    expect(finde("src/pages/Neu.tsx", src)).toEqual([
      { file: "src/pages/Neu.tsx", line: 2, was: "toLocale*String without a locale — use uiLocale()" },
    ]);
    expect(finde("x.tsx", "d.toLocaleTimeString([], { hour: '2-digit' })")).toHaveLength(1);
    expect(finde("x.tsx", "new Intl.NumberFormat().format(1)")).toHaveLength(1);
  });

  it("schlägt bei einem Locale-Literal an und nennt die Zeile", () => {
    const src = 'import x from "y";\n\nconst l = "en-US";\nconst m = "de-DE";\n';
    expect(finde("src/pages/Neu.tsx", src).map((f) => f.line)).toEqual([3, 4]);
  });

  it("lässt Aufrufe mit Locale durch", () => {
    const src = [
      "d.toLocaleString(uiLocale())",
      "d.toLocaleDateString(locale, { day: 'numeric' })",
      "new Intl.NumberFormat(uiLocale()).format(1)",
      "d.toLocaleTimeString(\n  uiLocale(),\n)",
    ].join("\n");
    expect(finde("x.tsx", src)).toEqual([]);
  });
});
