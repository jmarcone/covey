import { describe, expect, it } from "vitest";
import { erschaffeRegung } from "./regung";
import type { Leben } from "./leben";

/* A life reduced to what the comparison reads: where people and guests stand.
   `tick` is whatever the test makes it. */
function leben(figuren: { hier: number; x: number; y: number }[], gaeste: { etage: number; id: number; x: number; y: number }[] = []) {
  const l = {
    figuren: figuren.map((f) => ({ hier: f.hier, pos: { x: f.x, y: f.y } })),
    gaeste: gaeste.map((g) => ({ etage: g.etage, id: g.id, pos: { x: g.x, y: g.y } })),
    tick: () => {},
  };
  return l as unknown as Leben & { figuren: { hier: number; pos: { x: number; y: number } }[]; gaeste: { etage: number; id: number; pos: { x: number; y: number } }[] };
}

describe("what moved on the shown floor", () => {
  it("is false when the step left everyone where they were", () => {
    const l = leben([{ hier: 0, x: 10, y: 10 }], [{ etage: 0, id: 1, x: 5, y: 5 }]);
    expect(erschaffeRegung().schritt(l, 0, 0.25)).toBe(false);
  });

  it("sees a push apart, with nobody walking", () => {
    const l = leben([{ hier: 0, x: 100, y: 100 }, { hier: 0, x: 110, y: 100 }]);
    l.tick = () => {
      l.figuren[0].pos.x -= 10;
      l.figuren[1].pos.x += 10;
    };
    expect(erschaffeRegung().schritt(l, 0, 0.25)).toBe(true);
  });

  it("sees one step of the slowest walker on a 240 Hz display, and not a rounding error", () => {
    const l = leben([{ hier: 0, x: 100, y: 100 }]);
    l.tick = () => void (l.figuren[0].pos.x += 42 / 240);
    const r = erschaffeRegung();
    expect(r.schritt(l, 0, 1 / 240)).toBe(true);
    l.tick = () => void (l.figuren[0].pos.x += 0.001);
    expect(r.schritt(l, 0, 1 / 240)).toBe(false);
  });

  it("sees somebody arrive on the shown floor by the stairs, and somebody leave it", () => {
    const l = leben([{ hier: 1, x: 100, y: 100 }]);
    const r = erschaffeRegung();
    l.tick = () => void (l.figuren[0].hier = 0);
    expect(r.schritt(l, 0, 0.25)).toBe(true);
    l.tick = () => void (l.figuren[0].hier = 1);
    expect(r.schritt(l, 0, 0.25)).toBe(true);
  });

  it("does not care about what happens on another floor", () => {
    const l = leben([{ hier: 1, x: 100, y: 100 }], [{ etage: 1, id: 1, x: 0, y: 0 }]);
    l.tick = () => {
      l.figuren[0].pos.x += 50;
      l.gaeste[0].pos.x += 50;
    };
    expect(erschaffeRegung().schritt(l, 0, 0.25)).toBe(false);
  });

  it("sees a guest come, move and go on the shown floor", () => {
    const l = leben([]);
    const r = erschaffeRegung();
    l.tick = () => void l.gaeste.push({ etage: 0, id: 7, pos: { x: 1, y: 1 } });
    expect(r.schritt(l, 0, 0.25)).toBe(true);
    l.tick = () => void (l.gaeste[0].pos.x += 3);
    expect(r.schritt(l, 0, 0.25)).toBe(true);
    l.tick = () => void l.gaeste.splice(0, 1);
    expect(r.schritt(l, 0, 0.25)).toBe(true);
  });
});
