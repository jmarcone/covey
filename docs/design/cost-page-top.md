# Design: the top of the cost page (headline tiles and one filter bar)

Status: proposal, waiting for product-owner's acceptance. It covers design task 3cc742bc (the grouped agent filter) and the wish of 2026-11-18. Code references are to `main` @ e5edcabc (`web/src/pages/Costs.tsx`). `feat/cost-insights` is still not on the fork, so the parts that come from it are described from the human's screenshot of build cost-insights-e662903. Mockups (static HTML with covey's tokens and sample data) are in `docs/design/cost-page-top/`. Start with `1-default-light-1280.png`.

> Wish (2026-11-18): “This is bad: repeated info, bad use of space, and somehow the toggles and filter should modify all that is seen. Maybe grey out, or not, so the screen does not jump around.”

**The question this screen answers:** *What did the slice I picked cost, by each of the three measures, and how much work was it?* Above the fold at 1280 × 900: the title, **one filter bar**, **one row of seven tiles** and the cost chart. The second tile row inside “Cost over time”, “Total tokens” and the scope switch (`Costs.tsx:496-500`) go away.

## 1. Layout (1280 px, Costs as its own rail place: the content is 1149 px wide)
```
Costs                                             h1 22 px, lead line 14 px
──────────────────────────────────────────────────────────────────────────────── sticky, 53 px
[24 h|7 d|30 d|90 d]  [per day ▾]  [Agents  All agents ▾]  (■ Reported ✓)(■ Measured ✓)(■ Real ✓)
────────────────────────────────────────────────────────────────────────────────
┌■ Reported─┐┌■ Measured─┐┌■ Real─────┐┌List price ┐   ┌Input tok.┐┌Output tok┐┌Runs──────┐  92 px
│ 126.67 $  ││ 126.67 $  ││ 1.85 $    ││ per real $│   │ 83 M     ││ 1.2 M    ││ 75       │
│Claude's f.││0.0 % vs R.││plan 200$/mo││ 68×  52–81×│  │81 % cache││16 k / run││9 agents  │
└───────────┘└───────────┘└───────────┘└───────────┘   └──────────┘└──────────┘└──────────┘
┌ Cost over time      Show [Cost|Tokens]  Split [None|Agent|Department|Cost type]  [One chart|Per agent] ┐ 372 px
└───────────────────────────────────────────────────────────────────────────────────────────────────────┘
 By agent · Reported │ Usage per model · Reported │ Token mix │ Price list │ Priciest runs …
```
- **The bar sits above everything it controls,** full width, under the lead line, sticky with a hairline below. Nothing that filters lives anywhere else.
- **Chart-only controls stay on the chart card:** Show (Cost | Tokens), Split and Layout. They change only the chart. In the page bar they would repeat today's fault, a control that sits where it looks like it changes everything and then changes one card. **This deviates from point 4 of the brief, on purpose.** Layout “Per agent” is enabled only when Split = Agent.
- **The plan $/month is not in the bar.** It is an assumption of the Real figure, not a filter. It sits once, as the Real tile's sub-line (“plan 200 $/mo”, a button that opens a number field; Enter saves, Esc cancels). So the number appears once, next to the figure it changes.
- **One agent selector:** the picker in the bar replaces the scope switch (`Costs.tsx:496-500`) and the AgentFilter of `feat/cost-insights`. The scope name next to the h1 (`Costs.tsx:490`) goes too, because the picker already says it.

## 2. Filter bar
| Control | Look | Options and rules |
|---|---|---|
| Window | `.seg`, 32 px | 24 h · 7 d · 30 d · 90 d. Choosing one sets its default bucket (as `setRange`, `Costs.tsx:436`). |
| Bucket | dropdown “per day ▾”, 32 px | hour (window ≤ 7 d), day, week (≥ 30 d), month (90 d only). Options that don't fit the window stay in the list, disabled, with the reason as their title. |
| Agents | button 200 px fixed, summary ellipsised: “All agents”, “Platform (3)”, “Developer · covey”, “5 agents” | Popover 340 px: search; “All agents” (tri-state); departments with their count (tri-state, sorted by amount); their agents indented; “No department” last. Each row shows its amount in the **lead source** (§4) for the current window. Footer: “Amounts: Reported, last 30 d” and Clear. A change applies at once (no Apply button). Keys: arrows move, Space toggles, Esc closes. |
| Sources | 3 toggle chips, `aria-pressed`, swatch + name + ✓ | On: surface-2 fill, filled swatch, ✓. Off: no fill, hollow swatch, muted text, no ✓. At least one stays on: the last one on is disabled, with the reason as its title. |

**URL holds the whole state:** `?days=30&bucket=day&src=reported,measured,real&dept=<id>,…&agent=<id>,…&show=cost&split=none&layout=one`. Defaults are omitted, so `/costs` (the budget mail link, `notify.go:126`) opens the default view.

**Sizes. They change only by container width (container queries on the content column), never by data:**
| Content width | Bar | Tiles | Chart card |
|---|---|---|---|
| ≥ 1120 px (1280 viewport, own rail place) | 1 row, 53 px, sticky | 1 row of 7, 92 px high. Grid `repeat(4, 1.1fr) 0 repeat(3, .85fr)`, gap 12 px, 24 px between the money and usage groups. About 168 px / 130 px wide | 372 px |
| 900–1119 px (e.g. 1280 with today's Admin column = 909 px) | 1 row, 53 px | 2 rows: 4 money tiles, then 3 usage tiles, 92 px each, 12 px gap | 372 px |
| 640–899 px | 2 rows, 94 px (time controls; scope and sources) | as above | 372 px |
| < 640 px (390 viewport = 294 px content) | not sticky; 3 rows of 36 px, gap 8 = 124 px (window + bucket; agents; 3 sources) | money tiles 2 × 2 at 80 px; the usage tiles become **one list card of 3 rows × 34 px** (label · value · sub) | 300 px; Split becomes a dropdown, Layout is hidden |

## 3. Tiles (label · value · sub-line; each figure's exact value and formula are in the tooltip, on hover and on keyboard focus)
| # | Label | Value (formula) | Sub-line (formula) | Follows |
|---|---|---|---|---|
| 1 | ■ Reported | **R** = Σ `cost_entries.usd` in window × selection | “Claude's figure” (text) | window, agents, source |
| 2 | ■ Measured | **M** = Σ tokens × covey list price per model and token kind | “+0.4 % vs Reported” = (M − R) ÷ R; if Reported is off: “covey's recalculation” | window, agents, source |
| 3 | ■ Real | **Re** = Σ over accounts and plan periods of meter share × plan $/month × 12/52 ÷ 100 (build: “4 % of the week × 0.4615 $”) | “plan 200 $/mo”, the edit button | window, source; **whole organisation only** |
| 4 | List price per real $ | **X** = R ÷ Re for the window | range “52–81× per day” = min–max of R_b ÷ Re_b over buckets with Re_b > 0 | window, bucket, Reported + Real |
| 5 | Input tokens | **I** = Σ (input + cache_read + cache_creation) | “81 % cache hits” = cache_read ÷ I (**fixes** `Costs.tsx:448`, where cache writes counted as read from the cache) | window, agents |
| 6 | Output tokens | **O** = Σ output | “16 k per run” = O ÷ N | window, agents |
| 7 | Runs | **N** = number of `cost_entries` rows (today's `entries`) | “9 agents” = distinct `agent_id` with ≥ 1 entry | window, agents |

Tokens and runs do not depend on a source, so the source chips leave tiles 5–7 alone. When R and M agree, tiles 1 and 2 show the same amount. That is two measures agreeing, not a repeat, and the sub-line “0.0 % vs Reported” says so. No sub-line repeats its own value: the exact value lives only in the tooltip (the second line `exact(totals.output)` goes).

**States. Size and position never change:**
| State | Look | Value | Sub-line |
|---|---|---|---|
| source switched off (your choice) | no fill, hollow swatch, label muted | — | “switched off” |
| scope cannot answer: Real or X with an agent or department filter | grey tint `--tint`, no swatch fill change | — | “whole org only” · X: “needs Real” |
| no data | normal | money `0.00 $`, tokens `0`, runs `0` | Real without a meter reading: — / “no meter reading”; X with Re = 0: — / “needs Real” |
| first load | normal | 22 px skeleton bar (`--tint`) | empty, height kept; `aria-busy` |
| reloading after a filter change | previous values at 45 % opacity until the answer arrives; no skeleton flash | old | old |
| error | normal | — | “load failed” in `--text-danger`; one line under the bar: “Could not load costs. Retry” |

The amount of a switched-off source is not kept on screen. The wish says the toggles should change all that is seen, and a visible amount for a hidden source contradicts that. The dash keeps the place; the sub-line says why.

## 4. Number style (the whole cost page; `fmtUSD` stays as it is for the backlog card)
- **Money:** new `fmtMoney` in `format.ts`. Always 2 decimals, with format.ts grouping (`1,234.56 $` / `1.234,56 $`); `0.00 $`; `< 0.01 $` for 0 < v < 0.005. The tooltip has 4 decimals. This also covers the chart axis and tooltip, the lists, the picker and the priciest runs.
- **Tokens:** `fmtCount` as today (`8,421`, `12.3 k`, `1.2 M`, `83 M`); the tooltip has `exact()`.
- **Percent:** integer + nbsp + `%`; below 1: `< 1 %`. The Measured delta is signed, with one decimal (`+0.4 %`, `−0.4 %`, `0.0 %`).
- **Multiple:** `4.2×` below 10, `68×` from 10; the range uses an en dash (`52–81×`). All figures use `font-variant-numeric: tabular-nums`.
- **Lead source** (for anything that can show only one amount: the picker, By agent, By model, Priciest runs): Reported if it is on, else Measured. With only Real on, those lists show tokens and say so in their header.

## 5. Colour code (top part of the page)
| Colour | Meaning | Light / dark |
|---|---|---|
| `--src-reported` grey (new) | source Reported, the vendor's own figure | `#909097` / `#6c6c73` |
| `--src-measured` blue (new) | source Measured, covey's recalculation | `#2f63c0` / `#6593e3` |
| `--src-real` green (new) | source Real, the provider meter | `#1f8a57` / `#33ab71` |
| `--text-accent` | focus ring and the active picker | existing |
| `--text-danger` | load error text only | existing |

These are validated with the dataviz validator: normal-vision ΔE ≥ 17.8 and CVD ΔE ≥ 16.6 between neighbours in both themes. Grey has no chroma on purpose, because it is the neutral reference. Tritan ΔE is only 3.9, so **a swatch never appears without its source name**. The three tokens are used only on cost screens: they are not `--text-wait` or `--text-success`, so blue does not also mean “waiting” here, and green does not mean “done”. Departments get no colour on this page, because here colour means source.

## 6. What follows the bar below the headline
| Section | Window | Agents | Sources | Cannot / why |
|---|---|---|---|---|
| Cost over time | ✓ | ✓ | ✓ one line per source on | Real line only for the whole org |
| Usage per model (c4db5c57) | ✓ | ✓ | ✓ its own source toggles go; it uses the bar's | Real: no per-model value → — |
| By agent · By model | ✓ | ✓ (only the selected agents) | lead source | Real: no per-agent or per-model value |
| By credential | ✓ | ✓ | lead source | Real per credential exists, but only for the whole org |
| Price list (indicators) | ✓ | org or **one** agent today | Reported (unit prices come from `cost_entries`) | API takes one agent (`Costs.tsx:418-423`). For a department or several agents it needs a selection parameter; until then the card shows the grey “scope cannot answer” state. Reported off → “switched off” |
| Token mix | ✓ | ✓ | — (tokens have no source) | — |
| Priciest runs | ✓ | ✓ | lead source; M per run comes from its tokens | — |

**Backend need (for d5ac3678):** an agent-selection parameter (`agent=<id>,…`; departments are resolved to agents in the client) on `/cost/org`, `/cost/runs` and `/cost/indicators`. Today these take the whole org or one agent (`server.go:713-723`).

## 7. Formulas for cost-controller to confirm (d3dd6a34)
F1 Measured rates per model and token kind. F2 Real per plan period, and how a 30 d window splits weeks. F3 X uses R, not M, as the numerator. F4 Runs = rows of `cost_entries`: if 83d536fa shows that resumed runs book several rows, the label becomes “Cost entries” or the count changes. F5 Whether Real may ever be apportioned to agents (default: no, so it stays greyed).

## 8. Acceptance criteria
1. At 1280 the bar and the tile row show no figure twice: the plan amount once, each source once, no exact repeat on a second line.
2. Toggling each source, choosing a department, an agent, a window and a bucket changes every tile and section as §3 and §6 say. Tiles 5–7 do not react to the source chips.
3. Each tile's and the bar's `getBoundingClientRect()` is identical before and after every control is toggled, and while loading (layout shift 0) at 1440, 1280, 909 and 390 px.
4. With an agent or department filter, Real and X show the grey state with their reason. With a source off, its tile shows the hollow state.
5. Each figure has a tooltip with its exact value, formula and window, reachable by mouse and by Tab.
6. Money has 2 decimals everywhere on the page; `< 0.01 $` below a cent. The Input sub-line counts cache reads only.
7. There is one agent selector on the page. A reload restores the full state from the URL.
8. No tile text is ellipsised in en or de at 1280, 909 and 390 px. All 10 locale files have the new `costs.*` keys (parity test green). Budget: labels ≤ 22 characters, sub-lines ≤ 18 characters, then measured in de, fr and pl.
9. The light and dark themes both match the mockups. Every swatch stands next to its name.
