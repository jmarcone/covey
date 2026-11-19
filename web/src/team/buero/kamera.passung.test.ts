import * as THREE from "three";
import { describe, expect, it } from "vitest";
import type { Agent } from "../../api";
import { RAND, START_DREHUNG, START_NEIGUNG, erschaffeKamera, type Steuerung } from "./kamera";
import { hausBauen, type Gruppe, type Plan } from "./plan";
import { BAU_HOEHE } from "./szene";

/* The camera's fit, without a browser: a plan of a given size, a stage of a
 * given size, and where the eight corners of the building land in the
 * picture. Normalised device coordinates run from −1 to 1 across the stage,
 * so "inside with the margin" is a number.
 *
 * The cases come from houses the plan builder really makes — a floor of
 * eight people is 790 cm deep, one of a hundred and forty 2526 — because the
 * fit has to hold for every shape between them, not for one. */

const stage = (w: number, h: number) =>
  ({
    clientWidth: w,
    clientHeight: h,
    classList: { toggle() {} },
    getBoundingClientRect: () => ({ left: 0, top: 0, width: w, height: h }),
  }) as unknown as HTMLElement;

const leute = (n: number) =>
  Array.from({ length: n }, (_, i) => ({ id: `a${i}`, slug: `s${i}`, display_name: `A${i}` }) as Agent);

/** Every distinct floor of a house of `n` people. */
function plaene(n: number): Plan[] {
  const k = Math.max(2, Math.round(n / 6));
  const gruppen: Gruppe[] = Array.from({ length: k }, (_, i) => ({
    id: `d${i}`,
    name: `D${i}`,
    farbe: "",
    leute: leute(Math.ceil(n / k)),
  }));
  return hausBauen(gruppen, 3200, { besprechung: "B", kueche: "K", lounge: "L" }).etagen.map((e) => e.plan);
}

type Rand = { x0: number; x1: number; y0: number; y1: number };

/** Where the building's box lands in the picture, in −1…1. */
function projiziert(s: Steuerung, plan: Pick<Plan, "breite" | "hoehe">): Rand {
  const r = { x0: 9, x1: -9, y0: 9, y1: -9 };
  const v = new THREE.Vector3();
  for (const x of [-plan.breite / 2, plan.breite / 2]) for (const y of [0, BAU_HOEHE]) for (const z of [-plan.hoehe / 2, plan.hoehe / 2]) {
    v.set(x, y, z).project(s.kamera);
    r.x0 = Math.min(r.x0, v.x); r.x1 = Math.max(r.x1, v.x);
    r.y0 = Math.min(r.y0, v.y); r.y1 = Math.max(r.y1, v.y);
  }
  return r;
}

/** A stage whose size the test can change, and that keeps the listeners
 *  `anbinden` puts on it, so a wheel turn can be sent through them. */
function lebendeBuehne(w: number, h: number) {
  const el = { w, h };
  const hoerer = new Map<string, (e: unknown) => void>();
  const buehne = {
    get clientWidth() { return el.w; },
    get clientHeight() { return el.h; },
    classList: { toggle() {} },
    getBoundingClientRect: () => ({ left: 0, top: 0, width: el.w, height: el.h }),
    addEventListener: (art: string, f: (e: unknown) => void) => { hoerer.set(art, f); },
    removeEventListener: (art: string) => { hoerer.delete(art); },
    setPointerCapture() {},
  } as unknown as HTMLElement;
  const rad = (deltaX: number, deltaY: number) => {
    const e = { ctrlKey: false, metaKey: false, deltaX, deltaY, clientX: 0, clientY: 0, defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; } };
    hoerer.get("wheel")!(e);
    return e.defaultPrevented;
  };
  const doppelklick = (clientX: number, clientY: number) => hoerer.get("dblclick")!({ clientX, clientY });
  return { buehne, el, rad, doppelklick };
}

/** Where the camera looks, in height: it stands at the look target plus a
 *  fixed distance along the viewing direction. */
const blickHoehe = (s: Steuerung, plan: Pick<Plan, "breite" | "hoehe">) =>
  s.kamera.position.y - Math.sin(START_NEIGUNG) * Math.max(plan.breite, plan.hoehe) * 3;

/** Ticks until the ride has arrived; how often the arrival was reported. */
function faehrt(s: Steuerung): number {
  let angekommen = 0;
  for (let i = 0; i < 400; i++) if (s.tick(0.05)) angekommen++;
  return angekommen;
}

const GRENZE = 1 / (1 + RAND);
const BUEHNEN: [number, number][] = [[1000, 560], [1280, 560], [1920, 850], [900, 380], [280, 320], [294, 500]];

describe("the fit of the camera", () => {
  for (const n of [8, 24, 70, 140]) {
    for (const plan of plaene(n)) {
      for (const [w, h] of BUEHNEN) {
        it(`${n} people, floor ${plan.breite}×${plan.hoehe}, stage ${w}×${h}: whole building inside, same margin all round, in each of the four views`, () => {
          const s = erschaffeKamera(stage(w, h), true);
          s.planSetzen(plan);
          for (let k = 0; k < 4; k++) {
            const r = projiziert(s, plan);
            // Inside, with the margin — on both axes.
            expect(Math.max(-r.x0, r.x1)).toBeLessThanOrEqual(GRENZE + 1e-6);
            expect(Math.max(-r.y0, r.y1)).toBeLessThanOrEqual(GRENZE + 1e-6);
            // The same margin on the left as on the right, above as below.
            expect(r.x0 + r.x1).toBeCloseTo(0, 5);
            expect(r.y0 + r.y1).toBeCloseTo(0, 5);
            // And no band wider than the margin where the stage is the limit: the tighter axis reaches it.
            expect(Math.max(r.x1, r.y1)).toBeGreaterThanOrEqual(GRENZE - 1e-6);
            s.drehen(1);
            s.tick(10);
          }
        });
      }
    }
  }

  it("fits again when the stage is resized", () => {
    const plan = plaene(24)[0];
    const el = { w: 1280, h: 560 };
    const buehne = {
      get clientWidth() { return el.w; },
      get clientHeight() { return el.h; },
      classList: { toggle() {} },
      getBoundingClientRect: () => ({ left: 0, top: 0, width: el.w, height: el.h }),
    } as unknown as HTMLElement;
    const s = erschaffeKamera(buehne, true);
    s.planSetzen(plan);
    for (const [w, h] of [[1920, 850], [800, 600], [1280, 400]]) {
      el.w = w; el.h = h;
      s.groesse();
      const r = projiziert(s, plan);
      expect(Math.max(-r.x0, r.x1, -r.y0, r.y1)).toBeLessThanOrEqual(GRENZE + 1e-6);
      expect(Math.max(r.x1, r.y1)).toBeGreaterThanOrEqual(GRENZE - 1e-6);
      expect(r.x0 + r.x1).toBeCloseTo(0, 5);
      expect(r.y0 + r.y1).toBeCloseTo(0, 5);
    }
  });

  it("the start view cannot be panned away: nothing to reach, so the wheel goes to the page", () => {
    const plan = plaene(24)[0];
    const s = erschaffeKamera(stage(1280, 560), true);
    s.planSetzen(plan);
    const vorher = projiziert(s, plan);
    s.schieben(400, -300);
    s.schieben(-900, 700);
    expect(projiziert(s, plan)).toEqual(vorher);
    // Zoomed in it pans; back at the start scale it is centred again.
    s.zoomen(2);
    s.schieben(200, 100);
    expect(projiziert(s, plan)).not.toEqual(vorher);
    s.zoomen(0.5);
    expect(projiziert(s, plan).x0 + projiziert(s, plan).x1).toBeCloseTo(0, 5);
    s.zurueck();
    expect(projiziert(s, plan)).toEqual(vorher);
  });

  it("a tall, narrow stage starts a quarter turn on, and the building fills more of it", () => {
    const plan = plaene(24)[0];
    const breit = erschaffeKamera(stage(1280, 560), true);
    const hoch = erschaffeKamera(stage(280, 560), true);
    breit.planSetzen(plan); hoch.planSetzen(plan);
    expect(hoch.drehung - breit.drehung).toBeCloseTo(Math.PI / 2, 9);
    const flaeche = (s: Steuerung) => {
      const r = projiziert(s, plan);
      return ((r.x1 - r.x0) * (r.y1 - r.y0)) / 4;
    };
    const quer = erschaffeKamera(stage(280, 560), true);
    quer.planSetzen(plan);
    quer.drehen(-1); quer.tick(10); // turned back: the long side across the narrow stage
    expect(flaeche(hoch)).toBeGreaterThan(flaeche(quer));
    expect(START_NEIGUNG).toBeGreaterThan(0);
  });
  it("a stage that changes format turns the start view with it, and reports the arrival once", () => {
    const plan = plaene(24)[0];
    const { buehne, el } = lebendeBuehne(1280, 560);
    const s = erschaffeKamera(buehne);
    s.planSetzen(plan);
    expect(s.drehung).toBeCloseTo(START_DREHUNG, 9);
    // Wide → tall (a phone, a narrowed window): a quarter turn on, by travel.
    el.w = 280; el.h = 560;
    s.groesse();
    expect(s.drehung).toBeCloseTo(START_DREHUNG, 9); // nothing has moved yet
    expect(faehrt(s)).toBe(1);
    expect(s.drehung).toBeCloseTo(START_DREHUNG + Math.PI / 2, 9);
    // Tall → wide: back to where it started.
    el.w = 1280; el.h = 560;
    s.groesse();
    expect(faehrt(s)).toBe(1);
    expect(s.drehung).toBeCloseTo(START_DREHUNG, 9);
    // The same format again changes nothing.
    el.w = 1400; el.h = 600;
    s.groesse();
    expect(faehrt(s)).toBe(0);
  });

  it("a view somebody turned or zoomed keeps its rotation across a change of format", () => {
    const plan = plaene(24)[0];
    for (const eingriff of ["gedreht", "gezoomt"] as const) {
      const { buehne, el } = lebendeBuehne(1280, 560);
      const s = erschaffeKamera(buehne, true);
      s.planSetzen(plan);
      if (eingriff === "gedreht") { s.drehen(1); s.tick(10); } else s.zoomen(2);
      const vorher = s.drehung;
      el.w = 280; el.h = 560;
      s.groesse();
      expect(s.tick(10)).toBe(false);
      expect(s.drehung).toBe(vorher);
    }
  });

  it("at the start scale a sideways wheel turn is consumed, a vertical one goes on to the page", () => {
    const plan = plaene(24)[0];
    const { buehne, rad } = lebendeBuehne(1280, 560);
    const s = erschaffeKamera(buehne, true);
    s.planSetzen(plan);
    const loesen = s.anbinden(() => {});
    // A two-finger sideways swipe: the browser would take it for "back".
    expect(rad(80, 4)).toBe(true);
    expect(rad(-80, -4)).toBe(true);
    // Scrolling down to the neighbours of the stage: the page's.
    expect(rad(0, 120)).toBe(false);
    expect(rad(4, -80)).toBe(false);
    // Zoomed in the view moves, so both belong to the stage.
    s.zoomen(2);
    expect(rad(80, 4)).toBe(true);
    expect(rad(4, 80)).toBe(true);
    loesen();
  });
  it("arrow keys that find nothing to pan are left to the page; once zoomed in they are consumed", () => {
    const plan = plaene(24)[0];
    const s = erschaffeKamera(lebendeBuehne(1280, 560).buehne, true);
    s.planSetzen(plan);
    const loesen = s.anbinden(() => {});
    const taste = (key: string) => {
      const e = new KeyboardEvent("keydown", { key, cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(taste("ArrowDown")).toBe(false);
    expect(taste("ArrowLeft")).toBe(false);
    // The other keys are the stage's in every view.
    expect(taste("+")).toBe(true); // zoomed in now
    expect(taste("ArrowDown")).toBe(true);
    loesen();
  });

  it("a room and a double-clicked spot are looked at in the start view's height, not on the floor", () => {
    const plan = plaene(24)[0];
    const s = erschaffeKamera(stage(1280, 560), true);
    s.planSetzen(plan);
    s.zentrieren({ x: plan.breite / 4, y: plan.hoehe / 4 }, 1.6);
    expect(blickHoehe(s, plan)).toBeCloseTo(BAU_HOEHE / 2, 6);
    // The middle of that room, half a building up, is the middle of the picture.
    const v = new THREE.Vector3(plan.breite / 4 - plan.breite / 2, BAU_HOEHE / 2, plan.hoehe / 4 - plan.hoehe / 2).project(s.kamera);
    expect(v.x).toBeCloseTo(0, 6);
    expect(v.y).toBeCloseTo(0, 6);

    const { buehne, doppelklick } = lebendeBuehne(1280, 560);
    const t = erschaffeKamera(buehne, true);
    t.planSetzen(plan);
    const loesen = t.anbinden(() => {});
    t.zoomen(2);
    doppelklick(900, 200);
    expect(blickHoehe(t, plan)).toBeCloseTo(BAU_HOEHE / 2, 6);
    loesen();
  });
});
