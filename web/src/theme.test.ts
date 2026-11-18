import { describe, expect, it, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { THEME_KEY, gespeichertesTheme, initTheme, merkeTheme, wendeThemeAn } from "./theme";

/* The colors themselves belong to the stylesheet (light-dark() in styles.css)
   and are not testable here — jsdom does not compute them. Testable is the
   switch before them: what gets stored, and what then stands on the root element. */

describe("theme", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("ohne Wahl gilt System", () => {
    expect(gespeichertesTheme()).toBe("system");
  });

  it("unbekannter Wert im Speicher gilt als keine Wahl", () => {
    localStorage.setItem(THEME_KEY, "sepia");
    expect(gespeichertesTheme()).toBe("system");
  });

  it("merkt die Wahl und liest sie zurück", () => {
    merkeTheme("dark");
    expect(localStorage.getItem(THEME_KEY)).toBe("dark");
    expect(gespeichertesTheme()).toBe("dark");
  });

  it("setzt data-theme nur bei ausdrücklicher Wahl", () => {
    wendeThemeAn("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    wendeThemeAn("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    // "system" means: no attribute — then color-scheme in the stylesheet
    // decides, and a change of the system setting keeps breaking through.
    wendeThemeAn("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  it("wendet beim Start die gespeicherte Wahl an", () => {
    localStorage.setItem(THEME_KEY, "light");
    expect(initTheme()).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
});

/* The one place the stylesheet's colors are measurable without a browser: the
   light-dark() pairs are literals. "+ New task" wears --action-create, and the
   human's decision (#15) fixes what it has to measure: the label at 4.5:1 or
   better, the fill at 3:1 or better against the grounds it sits on, in both
   modes. */
describe("--action-create", () => {
  // Read as text: a stylesheet import comes back empty under vitest.
  const css = readFileSync(resolve(__dirname, "styles.css"), "utf8");
  const pair = (name: string): [string, string] => {
    const m = css.match(new RegExp(`--${name}:\\s*light-dark\\(\\s*(#[0-9a-f]{6})\\s*,\\s*(#[0-9a-f]{6})\\s*\\)`, "i"));
    if (!m) throw new Error(`--${name} is not a light-dark() pair of hex colors`);
    return [m[1], m[2]];
  };
  const lum = (hex: string) => {
    const [r, g, b] = [1, 3, 5].map((i) => {
      const c = parseInt(hex.slice(i, i + 2), 16) / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const ratio = (a: string, b: string) => {
    const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
    return (hi + 0.05) / (lo + 0.05);
  };
  // --on-accent is written as #fff; the measure wants six digits.
  const onAccent = (css.match(/--on-accent:\s*#([0-9a-f]{3})\b/i)?.[1] ?? "")
    .split("")
    .reduce((hex, c) => hex + c + c, "#");

  it.each([
    ["light", 0],
    ["dark", 1],
  ])("reads 4.5:1 for its label and 3:1 as a fill against every ground in %s", (_mode, i) => {
    const fill = pair("action-create")[i];
    expect(onAccent).toHaveLength(7);
    expect(ratio(fill, onAccent)).toBeGreaterThanOrEqual(4.5);
    for (const ground of ["surface-0", "surface-1", "surface-2"]) {
      expect(ratio(fill, pair(ground)[i]), ground).toBeGreaterThanOrEqual(3);
    }
  });

  // Not clay (the Working badge) and not red (Stop): blue.
  it("is neither the clay of the working badge nor the red of Stop", () => {
    const hue = (hex: string) => {
      const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
      const max = Math.max(r, g, b), d = max - Math.min(r, g, b);
      const h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
      return (h * 60 + 360) % 360;
    };
    for (const i of [0, 1]) {
      const h = hue(pair("action-create")[i]);
      expect(h).toBeGreaterThan(200);
      expect(h).toBeLessThan(260);
    }
  });
});
