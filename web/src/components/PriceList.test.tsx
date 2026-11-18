import { describe, it, expect, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import i18n from "../i18n";
import type { IndicatorReport } from "../api";
import { PriceList } from "./PriceList";

// #83: a unit price under a cent read "<$0.01 / unit" whatever it was, so two
// different prices looked the same. The price list shows two significant
// digits there, in the list and in the strip of the agent page.

const rep = (unit: number): IndicatorReport => ({
  indicators: [{ key: "review", title: "Reviews", count: 25, unit_usd: unit, prev_count: 20, series: [] }],
  failed: 0,
  total_usd: unit * 25,
  quality: { decided: 0, denied: 0 },
});

describe("PriceList: unit prices under a cent", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("shows the price with two significant digits, not <$0.01", () => {
    render(<PriceList rep={rep(0.0042)} />);
    expect(screen.getByText("$0.0042 / unit")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("<$0.01");
  });

  it("tells two different sub-cent prices apart", () => {
    const first = render(<PriceList rep={rep(0.0042)} />);
    const a = first.container.textContent;
    first.unmount();
    const second = render(<PriceList rep={rep(0.0007)} />);
    expect(second.container.textContent).not.toBe(a);
    expect(screen.getByText("$0.00070 / unit")).toBeInTheDocument();
  });

  it("does the same in the strip of the agent page", () => {
    render(<PriceList rep={rep(0.0042)} compact />);
    expect(screen.getByText("$0.0042 / unit")).toBeInTheDocument();
  });

  it("follows the German separators and symbol", async () => {
    await i18n.changeLanguage("de");
    render(<PriceList rep={rep(0.0042)} />);
    expect(screen.getByText("0,0042 $ / Stück")).toBeInTheDocument();
  });
});
