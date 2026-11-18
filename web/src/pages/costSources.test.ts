import { describe, it, expect } from "vitest";
import { DEFAULT_SOURCES, SOURCES, SOURCE_INFO, isPerModel, modelFigure } from "./costSources";
import type { ModelUsageRow } from "../api";

// The per-model table reads which sources it offers, which are on at first and
// which can have a figure per model from here, not from ids of its own.

describe("costSources", () => {
  it("has a granularity for every source: the engine's and the list's figures per model, the meter's and the bill's per credential", () => {
    expect(SOURCES.filter(isPerModel)).toEqual(["reported", "recomputed"]);
    expect(SOURCES.filter((s) => !isPerModel(s))).toEqual(["real", "billed"]);
    for (const s of SOURCES.filter((x) => !isPerModel(x))) expect(SOURCE_INFO[s].rowLabel).toBeTruthy();
  });

  it("turns on the per-model sources at first, so the table looks as it did", () => {
    expect(DEFAULT_SOURCES).toEqual(["reported", "recomputed"]);
  });

  it("gives a figure per model for the per-model sources only, null where the list has no price", () => {
    const r = { engine_usd: 2, list_usd: null } as ModelUsageRow;
    expect(modelFigure("reported", r)).toBe(2);
    expect(modelFigure("recomputed", r)).toBeNull();
    expect(modelFigure("recomputed", { ...r, list_usd: 3 })).toBe(3);
    expect(modelFigure("real", r)).toBeNull();
    expect(modelFigure("billed", r)).toBeNull();
  });
});
