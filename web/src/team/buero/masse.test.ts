import { describe, expect, it, vi } from "vitest";
import { erschaffeMasse } from "./masse";

class FalscherBeobachter {
  static letzter: FalscherBeobachter;
  ziele = new Set<Element>();
  constructor(public cb: (e: Partial<ResizeObserverEntry>[]) => void) {
    FalscherBeobachter.letzter = this;
  }
  observe(el: Element) {
    this.ziele.add(el);
  }
  unobserve(el: Element) {
    this.ziele.delete(el);
  }
  disconnect() {
    this.ziele.clear();
  }
}
const RO = FalscherBeobachter as unknown as typeof ResizeObserver;

describe("the cache of overlay sizes", () => {
  it("answers with the fallback until the first measurement, then with the measured size", () => {
    const geaendert = vi.fn();
    const m = erschaffeMasse(geaendert, RO);
    const el = document.createElement("div");
    m.beobachten("a", el);
    m.starten();
    expect(m.mass("a", { b: 110, h: 20 })).toEqual({ b: 110, h: 20 });
    FalscherBeobachter.letzter.cb([{ target: el, borderBoxSize: [{ inlineSize: 140.5, blockSize: 24 }] as unknown as ReadonlyArray<ResizeObserverSize> }]);
    expect(m.mass("a", { b: 110, h: 20 })).toEqual({ b: 140.5, h: 24 });
    expect(geaendert).toHaveBeenCalledTimes(1);
  });

  it("falls back to the content box where the border box is not reported", () => {
    const m = erschaffeMasse(() => {}, RO);
    const el = document.createElement("div");
    m.beobachten("a", el);
    m.starten();
    FalscherBeobachter.letzter.cb([{ target: el, contentRect: { width: 90, height: 18 } as DOMRectReadOnly }]);
    expect(m.mass("a", { b: 1, h: 1 })).toEqual({ b: 90, h: 18 });
  });

  it("observes what was registered before it started, and what comes after", () => {
    const m = erschaffeMasse(() => {}, RO);
    const a = document.createElement("div"), b = document.createElement("div");
    m.beobachten("a", a);
    m.starten();
    m.beobachten("b", b);
    expect([...FalscherBeobachter.letzter.ziele]).toEqual([a, b]);
  });

  it("does not observe the same element a second time, so a render costs no measurement", () => {
    const m = erschaffeMasse(() => {}, RO);
    const a = document.createElement("div");
    m.starten();
    const obs = FalscherBeobachter.letzter;
    const spy = vi.spyOn(obs, "observe");
    m.beobachten("a", a);
    m.beobachten("a", a);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("forgets an element that goes, and the size with it", () => {
    const m = erschaffeMasse(() => {}, RO);
    const a = document.createElement("div");
    m.starten();
    m.beobachten("a", a);
    FalscherBeobachter.letzter.cb([{ target: a, contentRect: { width: 50, height: 10 } as DOMRectReadOnly }]);
    m.beobachten("a", null);
    expect(FalscherBeobachter.letzter.ziele.size).toBe(0);
    expect(m.mass("a", { b: 1, h: 2 })).toEqual({ b: 1, h: 2 });
  });

  it("can be started again after it ended, and works without a ResizeObserver", () => {
    const m = erschaffeMasse(() => {}, RO);
    const a = document.createElement("div");
    m.beobachten("a", a);
    m.starten();
    m.beenden();
    m.starten();
    expect([...FalscherBeobachter.letzter.ziele]).toEqual([a]);
    const ohne = erschaffeMasse(() => {}, undefined);
    ohne.beobachten("a", a);
    ohne.starten();
    expect(ohne.mass("a", { b: 3, h: 4 })).toEqual({ b: 3, h: 4 });
  });
});
