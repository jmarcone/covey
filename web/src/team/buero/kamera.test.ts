import { describe, expect, it, vi } from "vitest";
import { erschaffeKamera } from "./kamera";
import type { Plan } from "./typen";

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
