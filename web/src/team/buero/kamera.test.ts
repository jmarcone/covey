import { afterEach, describe, expect, it, vi } from "vitest";
import * as THREE from "three";
import { erschaffeKamera, erschaffeMaler } from "./kamera";
import type { Plan } from "./typen";

/* jsdom has no WebGL: the renderer is a stand-in that keeps what it was told. */
const renderer = vi.hoisted(() => ({ gebaut: [] as Record<string, any>[] }));
vi.mock("three", async (orig) => {
  const echt = await orig<typeof import("three")>();
  return {
    ...echt,
    WebGLRenderer: class {
      domElement = document.createElement("canvas");
      shadowMap: Record<string, unknown> = { enabled: false, autoUpdate: true, type: -1 };
      pixelRatio = 0;
      setPixelRatio(r: number) { this.pixelRatio = r; }
      constructor() { renderer.gebaut.push(this as unknown as Record<string, any>); }
    },
  };
});

/* A stage whose size reads are counted: the camera must read the page when it
   is told to measure, and not whenever it fits itself. */
function buehne(b = 800, h = 500) {
  const el = document.createElement("div");
  const lesen = { n: 0, b, h };
  Object.defineProperty(el, "clientWidth", { get: () => (lesen.n++, lesen.b) });
  Object.defineProperty(el, "clientHeight", { get: () => (lesen.n++, lesen.h) });
  return { el, lesen };
}

describe("the camera", () => {
  const plan = { breite: 3000, hoehe: 1800 } as Plan; // the camera reads only the size

  it("tells the stage after every change of the view", () => {
    const { el } = buehne();
    const bewegt = vi.fn();
    const s = erschaffeKamera(el, false, bewegt);
    s.planSetzen(plan);
    bewegt.mockClear();
    s.zoomen(1.16);
    expect(bewegt).toHaveBeenCalledTimes(1);
    bewegt.mockClear();
    s.schieben(30, 10);
    expect(bewegt).toHaveBeenCalled();
    bewegt.mockClear();
    s.drehen(1);
    expect(bewegt).toHaveBeenCalledTimes(1);
    bewegt.mockClear();
    s.zurueck();
    expect(bewegt).toHaveBeenCalled();
    bewegt.mockClear();
    s.zentrieren({ x: 500, y: 500 }, 2);
    expect(bewegt).toHaveBeenCalled();
  });

  it("travels while a turn is under way and stands still when it has arrived", () => {
    const { el } = buehne();
    const s = erschaffeKamera(el, false);
    s.planSetzen(plan);
    expect(s.faehrt).toBe(false);
    s.drehen(1);
    expect(s.faehrt).toBe(true);
    let runden = 0;
    while (s.faehrt && runden++ < 200) s.tick(1 / 60);
    expect(s.faehrt).toBe(false);
    expect(runden).toBeLessThan(200);
  });

  it("snaps at once with reduced motion", () => {
    const { el } = buehne();
    const s = erschaffeKamera(el, true);
    s.planSetzen(plan);
    s.drehen(1);
    expect(s.tick(1 / 60)).toBe(true);
    expect(s.faehrt).toBe(false);
  });

  it("reports a reset across a quarter view as a ride, once", () => {
    const { el } = buehne();
    const s = erschaffeKamera(el, false);
    s.planSetzen(plan);
    s.drehen(1);
    while (s.faehrt) s.tick(1 / 60);
    s.zurueck();
    expect(s.faehrt).toBe(true);
    expect(s.tick(1 / 60)).toBe(true);
    expect(s.faehrt).toBe(false);
  });

  it("reads the stage's size only when it is measured, not when it fits itself", () => {
    const { el, lesen } = buehne();
    const s = erschaffeKamera(el, false);
    s.planSetzen(plan);
    lesen.n = 0;
    // Everything the frame and the handles do ...
    s.drehen(1);
    for (let i = 0; i < 30; i++) s.tick(1 / 60);
    s.zoomen(1.2);
    s.schieben(20, 5);
    s.stellen();
    expect(lesen.n).toBe(0);
    // ... and the one place that does read.
    expect(s.messen()).toEqual({ b: 800, h: 500 });
    expect(lesen.n).toBe(2);
  });

  it("fits the camera to the new aspect after a measurement", () => {
    const { el, lesen } = buehne(800, 500);
    const s = erschaffeKamera(el, false);
    s.planSetzen(plan);
    const aspekt = () => (s.kamera.right - s.kamera.left) / (s.kamera.top - s.kamera.bottom);
    expect(aspekt()).toBeCloseTo(800 / 500);
    lesen.b = 1600;
    expect(aspekt()).toBeCloseTo(800 / 500); // not before it is measured
    s.messen();
    expect(aspekt()).toBeCloseTo(1600 / 500);
  });
});

describe("the renderer's settings", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("caps the pixel ratio, draws the shadow map only on request and with the cheapest filter", () => {
    vi.stubGlobal("devicePixelRatio", 3);
    const { el } = buehne();
    expect(erschaffeMaler(el)).not.toBeNull();
    const m = renderer.gebaut.at(-1)!;
    expect(m.pixelRatio).toBeLessThanOrEqual(1.5);
    expect(m.shadowMap.enabled).toBe(true);
    expect(m.shadowMap.autoUpdate).toBe(false);
    expect(m.shadowMap.type).toBe(THREE.BasicShadowMap);
    expect(el.contains(m.domElement)).toBe(true);
  });

  it("keeps the display's own ratio when it is below the cap", () => {
    vi.stubGlobal("devicePixelRatio", 1);
    erschaffeMaler(buehne().el);
    expect(renderer.gebaut.at(-1)!.pixelRatio).toBe(1);
  });
});
