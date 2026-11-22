import { describe, it, expect, beforeEach, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { RangePanel } from "./RangePanel";
import type { DisplayTz } from "../format";
import { useGerman } from "../test/render";
import i18n from "../i18n";

// The calendar picker of the custom range. The tests run in Asia/Tokyo (UTC+9, vite.config.ts) and the clock is
// fixed: Friday 20 Nov 2026 17:30 UTC, which is already Saturday 21 Nov 02:30 in Tokyo. So every UTC day boundary
// here differs from the machine's, and a calendar that read days on the machine's clock would fail.

const NOW = Date.UTC(2026, 10, 20, 17, 30);
const H = 3_600_000;
const utc = (d: number, h = 0, mi = 0) => Date.UTC(2026, 10, d, h, mi);
const jst = (d: number, h = 0, mi = 0) => Date.UTC(2026, 10, d, h, mi) - 9 * H;

// The calendar's locale is a chunk of its own: each render waits for the grid.
async function panel(tz: DisplayTz, opts: { quickDays?: number | null; from?: number; to?: number } = {}) {
  const onApply = vi.fn();
  const r = render(
    <RangePanel from={opts.from ?? null} to={opts.to ?? null} tz={tz} onTz={() => {}} onApply={onApply}
      readings={[]} quickDays={opts.quickDays ?? null} now={() => NOW} />,
  );
  const day = (key: string) => {
    const b = r.container.querySelector<HTMLButtonElement>(`[data-day="${key}"] button`);
    if (!b) throw new Error(`no day ${key} in the calendar`);
    return b;
  };
  const cell = (key: string) => r.container.querySelector(`[data-day="${key}"]`)!;
  await within(r.container).findByRole("grid");
  return { ...r, onApply, day, cell };
}
const field = (name: RegExp) => screen.getByLabelText(name) as HTMLInputElement;
const apply = () => fireEvent.click(screen.getByRole("button", { name: "Übernehmen" }));

describe("Eigener Zeitraum — Kalender", () => {
  beforeEach(() => useGerman());

  it("wendet eine Voreinstellung sofort an, in UTC an UTC-Tagen", async () => {
    const p = await panel("utc");
    fireEvent.click(screen.getByRole("button", { name: "Gestern" }));
    expect(p.onApply).toHaveBeenLastCalledWith(utc(19), utc(20));
    fireEvent.click(screen.getByRole("button", { name: "Diese Woche" }));
    expect(p.onApply).toHaveBeenLastCalledWith(utc(16), NOW); // Monday 00:00 UTC to now
    fireEvent.click(screen.getByRole("button", { name: "Letzter Monat" }));
    expect(p.onApply).toHaveBeenLastCalledWith(Date.UTC(2026, 9, 1), utc(1));
    // The fields show what was applied, in UTC.
    expect(field(/^Von/).value).toBe("2026-10-01T00:00");
    expect(field(/^Bis/).value).toBe("2026-11-01T00:00");
  });

  it("wendet eine Voreinstellung in Ortszeit an den Tagen des Lesers an", async () => {
    const p = await panel("local");
    fireEvent.click(screen.getByRole("button", { name: "Gestern" }));
    // Tokyo's yesterday is the 20th, from 00:00 to 00:00 there: 15:00 UTC the day before.
    expect(p.onApply).toHaveBeenLastCalledWith(jst(20), jst(21));
    fireEvent.click(screen.getByRole("button", { name: "Letzte 7 Tage" }));
    expect(p.onApply).toHaveBeenLastCalledWith(jst(15), NOW);
  });

  it("nennt den Grund, wenn kein Reset bekannt ist", async () => {
    await panel("utc");
    const b = screen.getByRole("button", { name: "Seit dem letzten Reset" });
    expect(b).toBeDisabled();
    expect(b).toHaveAccessibleDescription("Es ist noch kein Reset des Wochenzählers bekannt.");
  });

  it("füllt mit zwei Klicks im Kalender die Felder und übernimmt erst mit „Übernehmen“ [Tagesanfang, Anfang des Folgetags)", async () => {
    const p = await panel("utc");
    fireEvent.click(p.day("2026-11-19"));
    fireEvent.click(p.day("2026-11-17")); // either order
    expect(p.onApply).not.toHaveBeenCalled();
    expect(field(/^Von/).value).toBe("2026-11-17T00:00");
    expect(field(/^Bis/).value).toBe("2026-11-20T00:00");
    for (const d of ["2026-11-17", "2026-11-18", "2026-11-19"]) expect(p.cell(d)).toHaveAttribute("aria-selected", "true");
    expect(p.cell("2026-11-20")).not.toHaveAttribute("aria-selected");
    expect(screen.getByText(/^Von Di\., 17\. Nov\., 00:00 UTC bis Fr\., 20\. Nov\., 00:00 UTC · 3 Tage \(72:00 h\)$/)).toBeInTheDocument();
    apply();
    expect(p.onApply).toHaveBeenCalledWith(utc(17), utc(20));
  });

  it("liest die Kalendertage in Ortszeit an den Tagen des Lesers", async () => {
    const p = await panel("local");
    fireEvent.click(p.day("2026-11-17"));
    fireEvent.click(p.day("2026-11-19"));
    apply();
    expect(p.onApply).toHaveBeenCalledWith(jst(17), jst(20));
  });

  it("nimmt einen einzelnen Tag nach dem ersten Klick als ganzen Tag", async () => {
    const p = await panel("utc");
    fireEvent.click(p.day("2026-11-18"));
    expect(screen.getByText("Jetzt den letzten Tag wählen – oder diesen einen Tag übernehmen.")).toBeInTheDocument();
    apply();
    expect(p.onApply).toHaveBeenCalledWith(utc(18), utc(19));
  });

  it("endet mit einer Endzeit an dieser Minute des letzten Tages, mit einer Startzeit an der des ersten", async () => {
    const p = await panel("utc");
    fireEvent.click(p.day("2026-11-17"));
    fireEvent.click(p.day("2026-11-19"));
    expect(field(/^Endzeit/).value).toBe(""); // empty: to the end of the day
    fireEvent.change(field(/^Startzeit/), { target: { value: "08:15" } });
    fireEvent.change(field(/^Endzeit/), { target: { value: "12:30" } });
    expect(field(/^Von/).value).toBe("2026-11-17T08:15");
    expect(field(/^Bis/).value).toBe("2026-11-19T12:30");
    apply();
    expect(p.onApply).toHaveBeenCalledWith(utc(17, 8, 15), utc(19, 12, 30));
    // Emptied again, the end is the day's end.
    fireEvent.change(field(/^Endzeit/), { target: { value: "" } });
    expect(field(/^Bis/).value).toBe("2026-11-20T00:00");
  });

  it("liest 00:00, in das Endzeit-Feld getippt, als diese Minute: das Feld bleibt, der Kalender auch", async () => {
    const p = await panel("utc");
    fireEvent.click(p.day("2026-11-17"));
    fireEvent.click(p.day("2026-11-19"));
    fireEvent.change(field(/^Endzeit/), { target: { value: "00:00" } });
    expect(field(/^Endzeit/).value).toBe("00:00");
    expect(field(/^Bis/).value).toBe("2026-11-19T00:00");
    expect(p.cell("2026-11-19")).toHaveAttribute("aria-selected", "true");
    expect(p.cell("2026-11-18")).toHaveAttribute("aria-selected", "true");
    fireEvent.change(field(/^Endzeit/), { target: { value: "00:30" } });
    expect(field(/^Bis/).value).toBe("2026-11-19T00:30");
    apply();
    expect(p.onApply).toHaveBeenCalledWith(utc(17), utc(19, 0, 30));
    // An end at a day's start not typed there (the exact field) is still the whole day before.
    fireEvent.change(field(/^Bis/), { target: { value: "2026-11-19T00:00" } });
    expect(field(/^Endzeit/).value).toBe("");
    expect(p.cell("2026-11-19")).not.toHaveAttribute("aria-selected");
  });

  it("schreibt fest über onFreeze, wo die Seite es gibt", async () => {
    const onApply = vi.fn(), onFreeze = vi.fn();
    render(<RangePanel from={null} to={null} tz="utc" onTz={() => {}} onApply={onApply} onFreeze={onFreeze}
      readings={[]} quickDays={7} now={() => NOW} />);
    fireEvent.click(screen.getByRole("button", { name: "„7 Tage“ auf jetzt festschreiben" }));
    expect(onFreeze).toHaveBeenCalledWith(NOW - 7 * 24 * H, NOW);
    expect(onApply).not.toHaveBeenCalled();
  });

  it("lässt die beiden Live-Regionen auch leer im Dokument", async () => {
    const p = await panel("utc");
    // The calendar's hint (no range being picked) and the summary are there, empty or not.
    expect(p.container.querySelector("[data-range-calendar] > p[aria-live=polite]")!.textContent).toBe("");
    expect(p.container.querySelector("[data-range-summary]")).toHaveAttribute("aria-live", "polite");
    fireEvent.change(field(/^Bis/), { target: { value: "" } }); // no summary
    expect(p.container.querySelector("[data-range-summary]")).toBeInTheDocument();
    expect(p.container.querySelector("[data-range-summary]")!.textContent).toBe("");
  });

  it("folgt den genauen Feldern: was dort steht, markiert der Kalender", async () => {
    const p = await panel("utc");
    fireEvent.change(field(/^Von/), { target: { value: "2026-11-10T00:00" } });
    fireEvent.change(field(/^Bis/), { target: { value: "2026-11-12T00:00" } });
    expect(p.cell("2026-11-10")).toHaveAttribute("aria-selected", "true");
    expect(p.cell("2026-11-11")).toHaveAttribute("aria-selected", "true");
    expect(p.cell("2026-11-12")).not.toHaveAttribute("aria-selected");
    expect(field(/^Endzeit/).value).toBe("");
  });

  it("sperrt Tage in der Zukunft der gewählten Zone", async () => {
    const u = await panel("utc");
    expect(u.day("2026-11-20")).toBeEnabled(); // today in UTC
    expect(u.cell("2026-11-21")).toHaveAttribute("data-disabled", "true");
    expect(u.cell("2026-11-20")).toHaveAttribute("data-today", "true");
    u.unmount();
    const l = await panel("local");
    expect(l.day("2026-11-21")).toBeEnabled(); // today in Tokyo
    expect(l.cell("2026-11-22")).toHaveAttribute("data-disabled", "true");
    expect(l.cell("2026-11-21")).toHaveAttribute("data-today", "true");
  });

  it("schneidet ein Ende in der Zukunft auf jetzt", async () => {
    const p = await panel("utc");
    fireEvent.click(p.day("2026-11-19"));
    fireEvent.click(p.day("2026-11-20")); // today: the day's end is tomorrow 00:00
    apply();
    expect(p.onApply).toHaveBeenCalledWith(utc(19), NOW);
    expect(field(/^Bis/).value).toBe("2026-11-20T17:30");
  });

  it("macht aus 30 Tagen einen festen Zeitraum, der jetzt endet", async () => {
    const p = await panel("utc", { quickDays: 30 });
    const b = screen.getByRole("button", { name: "„30 Tage“ auf jetzt festschreiben" });
    expect(b).toHaveAccessibleDescription(/ein geteilter Link zeigt dann immer denselben Zeitraum/i);
    fireEvent.click(b);
    expect(p.onApply).toHaveBeenCalledWith(NOW - 30 * 24 * H, NOW);
  });

  it("kann 90 Tage nicht festschreiben und sagt warum; ohne Schnellbereich gibt es die Aktion nicht", async () => {
    const p = await panel("utc", { quickDays: 90 });
    const b = screen.getByRole("button", { name: "„90 Tage“ auf jetzt festschreiben" });
    expect(b).toBeDisabled();
    expect(b).toHaveAccessibleDescription("90 Tage sind länger als 31 Tage, mehr umfasst ein fester Zeitraum nicht.");
    p.unmount();
    await panel("utc", { from: utc(17), to: utc(18) });
    expect(document.querySelector("[data-range-freeze]")).toBeNull();
  });

  it("zeigt den Kalender auf Deutsch, die Woche ab Montag, als beschriftetes Raster", async () => {
    const p = await panel("utc");
    const grid = screen.getByRole("grid", { name: "November 2026" });
    // The weekday row is hidden from the accessibility tree (each day button names its weekday): read it as drawn.
    const heads = grid.querySelectorAll("th.rdp-weekday");
    expect(heads).toHaveLength(7);
    expect(within(grid).getByRole("button", { name: "Dienstag, 17. November 2026" })).toBe(p.day("2026-11-17"));
    expect(heads[0]).toHaveAttribute("aria-label", "Montag");
    expect(heads[6]).toHaveAttribute("aria-label", "Sonntag");
    expect(screen.getByRole("button", { name: "Zum vorherigen Monat" })).toBeInTheDocument();
    // No focusable inside another: the day buttons sit in cells that are not focusable themselves.
    expect(p.cell("2026-11-17").getAttribute("tabindex")).toBeNull();
  });

  it("folgt der Sprache der Oberfläche, auch auf Englisch mit Montag zuerst", async () => {
    i18n.changeLanguage("en");
    await panel("utc");
    const grid = screen.getByRole("grid", { name: "November 2026" });
    expect(grid.querySelector("th.rdp-weekday")).toHaveAttribute("aria-label", "Monday");
    expect(screen.getByRole("button", { name: "Last 7 days" })).toBeInTheDocument();
  });

  it("beschreibt alle Felder mit dem Fehler, als Alarm", async () => {
    await panel("utc");
    fireEvent.change(field(/^Von/), { target: { value: "2026-11-19T10:00" } });
    fireEvent.change(field(/^Bis/), { target: { value: "2026-11-19T09:00" } });
    apply();
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toBe("Das Ende muss nach dem Anfang liegen.");
    for (const f of [/^Von/, /^Bis/, /^Startzeit/, /^Endzeit/]) {
      expect(field(f)).toHaveAttribute("aria-invalid", "true");
      expect(field(f).getAttribute("aria-describedby")).toContain(alert.id);
    }
  });
});
