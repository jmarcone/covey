import { describe, it, expect } from "vitest";
import i18next from "i18next";
import de from "./de.json";
import en from "./en.json";
import es from "./es.json";
import fr from "./fr.json";
import it_ from "./it.json";
import ja from "./ja.json";
import nl from "./nl.json";
import pl from "./pl.json";
import pt from "./pt.json";
import zh from "./zh.json";

/* The parity test compares the catalogues with English, so a key that goes
   from all ten at once (a merge that takes one side of every file) passes it.
   The strings the organisation path and the department filter stand on are
   named here, so that losing them is a red test. */
const KEYS = [
  "agent.crumb.label",
  "agent.crumb.more_one",
  "agent.crumb.more_other",
  "agent.crumb.between",
  "agent.crumb.allIn",
  "agent.crumb.allWithout",
  "agent.crumb.reportsTo",
  "dashboard.deptFilter",
  "dashboard.deptFilterRemove",
  "dashboard.deptUnknown",
  "dashboard.deptEmpty",
  "dashboard.deptEmptyNone",
  "dashboard.allAgents",
];
const kataloge: [string, unknown][] = [["de", de], ["en", en], ["es", es], ["fr", fr], ["it", it_], ["ja", ja], ["nl", nl], ["pl", pl], ["pt", pt], ["zh", zh]];

describe("The crumb and the department filter", () => {
  it.each(kataloge)("have their strings in %s", (_sprache, katalog) => {
    const fehlt = KEYS.filter((k) => typeof k.split(".").reduce<any>((o, p) => o?.[p], katalog) !== "string");
    expect(fehlt).toEqual([]);
  });
});

/* Polish has four plural forms, and i18next falls back to `_other` for one
   that is missing: „Pokaż jeszcze 2 poziomów“ is wrong, „2 poziomy“ is right.
   A count key in pl.json therefore carries all four. */
describe("Polish plurals of the crumb's fold", () => {
  const t = async () => {
    const i = i18next.createInstance();
    await i.init({ lng: "pl", resources: { pl: { translation: pl } }, interpolation: { escapeValue: false } });
    return (count: number) => i.t("agent.crumb.more", { count });
  };

  it("uses the form of the number", async () => {
    const more = await t();
    expect(more(1)).toBe("Pokaż jeszcze 1 poziom");
    expect(more(2)).toBe("Pokaż jeszcze 2 poziomy");
    expect(more(4)).toBe("Pokaż jeszcze 4 poziomy");
    expect(more(5)).toBe("Pokaż jeszcze 5 poziomów");
    expect(more(22)).toBe("Pokaż jeszcze 22 poziomy");
  });
});
