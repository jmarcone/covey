import { describe, expect, it, vi } from "vitest";
import { erschaffeTakt, LEBEN_FPS, RUHE_MS, sichtWaechter, type Bedarf, type Uhr } from "./takt";

/* The browser's clocks, held in the hand: what has been asked for (`rahmen`)
   and what waits for its time (`fristen`) are both visible, and time passes
   only when the test says so. */
function falscheUhr(spaet = 0) {
  let jetzt = 1000;
  let n = 0;
  const rahmen = new Map<number, () => void>();
  const fristen = new Map<number, { an: number; cb: () => void }>();
  const uhr: Uhr = {
    jetzt: () => jetzt,
    rahmen: (cb) => (rahmen.set(++n, cb), n),
    rahmenAbbrechen: (id) => void rahmen.delete(id),
    frist: (cb, ms) => (fristen.set(++n, { an: jetzt + ms + spaet, cb }), n),
    fristAbbrechen: (id) => void fristen.delete(id),
  };
  /** The display's next frame, if one was requested. */
  const naechsterRahmen = () => {
    const [id, cb] = [...rahmen][0] ?? [];
    if (id === undefined) return false;
    rahmen.delete(id);
    cb!();
    return true;
  };
  /** Lets `ms` pass; every timer that falls due runs, and every frame it asks for. */
  const vergehen = (ms: number, mitRahmen = true) => {
    const bis = jetzt + ms;
    for (;;) {
      const faellig = [...fristen].filter(([, f]) => f.an <= bis).sort((a, b) => a[1].an - b[1].an)[0];
      if (!faellig) break;
      jetzt = Math.max(jetzt, faellig[1].an);
      fristen.delete(faellig[0]);
      faellig[1].cb();
      while (mitRahmen && naechsterRahmen());
    }
    jetzt = bis;
  };
  return { uhr, rahmen, fristen, naechsterRahmen, vergehen, vor: (ms: number) => (jetzt += ms) };
}

describe("the office's beat", () => {
  it("queues no frame, and no timer, once nothing moves", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "ruht");
    const takt = erschaffeTakt({ bild, uhr: u.uhr });
    expect(u.rahmen.size).toBe(1); // the first picture
    u.naechsterRahmen();
    expect(bild).toHaveBeenCalledTimes(1);
    expect(u.rahmen.size).toBe(0);
    expect(u.fristen.size).toBe(0);
    expect(takt.wartet).toBe(false);
    u.vergehen(10_000);
    expect(bild).toHaveBeenCalledTimes(1);
  });

  it("draws exactly one more frame after a change, however many nudges", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "ruht");
    const takt = erschaffeTakt({ bild, uhr: u.uhr });
    u.naechsterRahmen();
    takt.anstossen();
    takt.anstossen();
    takt.anstossen();
    expect(u.rahmen.size).toBe(1);
    u.naechsterRahmen();
    expect(bild).toHaveBeenCalledTimes(2);
    expect(u.rahmen.size).toBe(0);
  });

  it("keeps the display's pace on \"weiter\" until the ride is over", () => {
    const u = falscheUhr();
    let rest = 3;
    const bild = vi.fn((): Bedarf => (--rest > 0 ? "weiter" : "ruht"));
    erschaffeTakt({ bild, uhr: u.uhr });
    expect(u.naechsterRahmen()).toBe(true);
    expect(u.naechsterRahmen()).toBe(true);
    expect(u.naechsterRahmen()).toBe(true);
    expect(u.naechsterRahmen()).toBe(false);
    expect(bild).toHaveBeenCalledTimes(3);
  });

  it("draws the ambient life alone at 30 frames per second at most", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "leben");
    erschaffeTakt({ bild, uhr: u.uhr });
    u.naechsterRahmen();
    // The display would offer sixty frames; the takt waits for its timer first.
    u.vergehen(1000);
    expect(bild.mock.calls.length).toBeLessThanOrEqual(LEBEN_FPS + 5);
    expect(bild.mock.calls.length).toBeGreaterThan(LEBEN_FPS / 2);
  });

  it("reaches 30 frames per second on a 60 Hz display, not 20", () => {
    // Frames run only at the display's vsync, and a timer fires a millisecond
    // late, as it does in a browser. A timer aimed at exactly 33.3 ms would
    // land just after a vsync, and its frame a whole refresh later.
    const u = falscheUhr(1);
    const bild = vi.fn((): Bedarf => "leben");
    erschaffeTakt({ bild, uhr: u.uhr });
    u.naechsterRahmen();
    const vsync = 1000 / 60;
    for (let t = 0; t < 1000; t += vsync) {
      u.vergehen(vsync, false);
      u.naechsterRahmen();
    }
    expect(bild.mock.calls.length).toBeGreaterThanOrEqual(LEBEN_FPS - 2);
    expect(bild.mock.calls.length).toBeLessThanOrEqual(LEBEN_FPS + 2);
  });

  it("gives the life its time without a frame and wakes one only when something starts to move", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "ruht");
    let geht = false;
    const dts: number[] = [];
    const leise = vi.fn((dt: number) => (dts.push(dt), geht));
    erschaffeTakt({ bild, leise, uhr: u.uhr });
    u.naechsterRahmen();
    // Nothing is queued with the display, only the coarse timer for the life.
    expect(u.rahmen.size).toBe(0);
    u.vergehen(RUHE_MS * 4);
    expect(leise).toHaveBeenCalledTimes(4);
    expect(bild).toHaveBeenCalledTimes(1);
    expect(u.rahmen.size).toBe(0);
    expect(dts.every((d) => Math.abs(d - RUHE_MS / 1000) < 1e-9)).toBe(true);
    // Somebody gets up: now a picture is wanted.
    geht = true;
    u.vergehen(RUHE_MS);
    expect(bild).toHaveBeenCalledTimes(2);
  });

  it("has no timer either without `leise` (reduced motion)", () => {
    const u = falscheUhr();
    erschaffeTakt({ bild: () => "ruht", uhr: u.uhr });
    u.naechsterRahmen();
    expect(u.rahmen.size + u.fristen.size).toBe(0);
  });

  it("does not spend a second frame on a change made while a frame is drawn", () => {
    const u = falscheUhr();
    let takt!: ReturnType<typeof erschaffeTakt>;
    const bild = vi.fn((): Bedarf => (takt.anstossen(), "ruht"));
    takt = erschaffeTakt({ bild, uhr: u.uhr });
    u.naechsterRahmen();
    expect(u.rahmen.size).toBe(0);
  });

  it("stops while the tab is hidden and draws once on return", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "leben");
    const takt = erschaffeTakt({ bild, leise: () => false, uhr: u.uhr });
    u.naechsterRahmen();
    expect(takt.wartet).toBe(true);
    takt.sichtbar("tab", false);
    expect(takt.wartet).toBe(false);
    expect(u.rahmen.size + u.fristen.size).toBe(0);
    takt.anstossen(); // a change while hidden queues nothing
    u.vergehen(5000);
    expect(bild).toHaveBeenCalledTimes(1);
    takt.sichtbar("tab", true);
    expect(u.rahmen.size).toBe(1);
    u.naechsterRahmen();
    expect(bild).toHaveBeenCalledTimes(2);
  });

  it("stops when the stage is out of view and runs again only when both hold", () => {
    const u = falscheUhr();
    const bild = vi.fn((): Bedarf => "ruht");
    const takt = erschaffeTakt({ bild, leise: () => false, uhr: u.uhr });
    u.naechsterRahmen();
    takt.sichtbar("bild", false);
    takt.sichtbar("tab", false);
    u.vergehen(2000);
    expect(bild).toHaveBeenCalledTimes(1);
    takt.sichtbar("tab", true); // the tab is back, the stage is still out of view
    expect(u.rahmen.size).toBe(0);
    takt.sichtbar("bild", true);
    expect(u.rahmen.size).toBe(1);
  });

  it("gives the life one short step after a pause, not all the time that passed", () => {
    const u = falscheUhr();
    const roh: number[] = [];
    const takt = erschaffeTakt({ bild: (dt) => (roh.push(dt), "ruht"), uhr: u.uhr });
    u.naechsterRahmen();
    takt.sichtbar("tab", false);
    u.vor(60_000);
    takt.sichtbar("tab", true);
    u.naechsterRahmen();
    expect(roh[1]).toBeLessThanOrEqual(0.064);
    expect(roh[1]).toBeGreaterThanOrEqual(0);
  });

  it("clears everything on end", () => {
    const u = falscheUhr();
    const takt = erschaffeTakt({ bild: () => "leben", leise: () => false, uhr: u.uhr });
    u.naechsterRahmen();
    takt.beenden();
    takt.anstossen();
    expect(u.rahmen.size + u.fristen.size).toBe(0);
  });
});

describe("the visibility watcher", () => {
  class FalscherBeobachter {
    static letzter: FalscherBeobachter;
    ziele: Element[] = [];
    getrennt = false;
    constructor(public cb: (e: { isIntersecting: boolean }[]) => void) {
      FalscherBeobachter.letzter = this;
    }
    observe(el: Element) {
      this.ziele.push(el);
    }
    disconnect() {
      this.getrennt = true;
    }
  }
  const io = FalscherBeobachter as unknown as typeof IntersectionObserver;

  function dokument() {
    const hoerer = new Map<string, () => void>();
    return {
      hidden: false,
      addEventListener: (n: string, f: () => void) => void hoerer.set(n, f),
      removeEventListener: (n: string) => void hoerer.delete(n),
      hoerer,
    };
  }

  it("reports the hidden tab and its return", () => {
    const takt = { sichtbar: vi.fn() };
    const doc = dokument();
    sichtWaechter(document.createElement("div"), takt, doc as unknown as Document, undefined);
    doc.hidden = true;
    doc.hoerer.get("visibilitychange")!();
    doc.hidden = false;
    doc.hoerer.get("visibilitychange")!();
    expect(takt.sichtbar.mock.calls).toEqual([["tab", false], ["tab", true]]);
  });

  it("reports the stage leaving the viewport and coming back", () => {
    const takt = { sichtbar: vi.fn() };
    const buehne = document.createElement("div");
    sichtWaechter(buehne, takt, dokument() as unknown as Document, io);
    expect(FalscherBeobachter.letzter.ziele).toEqual([buehne]);
    FalscherBeobachter.letzter.cb([{ isIntersecting: false }]);
    FalscherBeobachter.letzter.cb([{ isIntersecting: false }, { isIntersecting: true }]);
    expect(takt.sichtbar.mock.calls).toEqual([["bild", false], ["bild", true]]);
  });

  it("lets go of both on release", () => {
    const takt = { sichtbar: vi.fn() };
    const doc = dokument();
    const los = sichtWaechter(document.createElement("div"), takt, doc as unknown as Document, io);
    los();
    expect(FalscherBeobachter.letzter.getrennt).toBe(true);
    expect(doc.hoerer.size).toBe(0);
  });

  it("counts the stage as visible without an IntersectionObserver", () => {
    const takt = { sichtbar: vi.fn() };
    sichtWaechter(document.createElement("div"), takt, dokument() as unknown as Document, undefined);
    expect(takt.sichtbar).not.toHaveBeenCalled();
  });

  it("reports a tab that is hidden from the start", () => {
    const takt = { sichtbar: vi.fn() };
    const doc = dokument();
    doc.hidden = true;
    sichtWaechter(document.createElement("div"), takt, doc as unknown as Document, undefined);
    expect(takt.sichtbar).toHaveBeenCalledWith("tab", false);
  });
});
