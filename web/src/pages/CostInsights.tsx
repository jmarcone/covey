import { Fragment, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type ExternalModelUsage, type ExternalUsage, type ModelUsageReport, type ModelUsageRow, type UsageReading } from "../api";
import { fmtBucket, fmtCount, fmtNumber, fmtUSD, fmtUSDBelow, fmtUSDFixed } from "../format";
import { DEFAULT_SOURCES, SOURCES, SOURCE_INFO, isPerModel, modelFigure, sourceHint, sourceLabel, type Source } from "./costSources";
import { isSeat, perCredentialTotals, scopeFigure, type SourceTotal } from "./costFigures";
import { usageList } from "./CostBasis";
import { Chip } from "./StackedCostChart";
import { usableReadings, type CredentialFigures } from "./costFigures";
import { OrgWide, OutsideMark, SOURCE_COLOR, Swatch, useUsageReadings } from "./StackedCostChart";
import { externalStamp, listedSources, outsideModelRows, sourceStyles, type OutsideModelRow, type SourceStyles } from "./outsideFigures";
import { withUnpriced, withinMoney } from "./StackedCostChart";

/* Cost insights.
 *
 * Two questions the totals above cannot answer:
 *  1. What did each MODEL consume? Claude Code reports usage per model on
 *     every result line — an Opus run also makes small Haiku calls — with the
 *     cache writes split by lifetime. The table sets Claude Code's own price
 *     (reported) next to the same tokens priced with covey's list
 *     (recomputed): a price-table check. Each money source is a column that
 *     can be switched off, as in the chart. The real cost and the bill are
 *     per credential, not per model: each has a row of its own under the
 *     total, beside the list price of the same credentials over the same
 *     span, so that no row sets figures of different scopes side by side.
 *  2. What does a point of the subscription meter buy? Per credential, the
 *     same figures as its tiles above (costFigures.ts): the meter's span,
 *     the real cost of its movement, the list price of the credential's
 *     lines over the same span, and the multiple. The meter is
 *     account-wide: other use of the account moves it too. */

const th: React.CSSProperties = { padding: "4px 8px 8px 0", fontWeight: 500, whiteSpace: "nowrap" };
const td: React.CSSProperties = { padding: "4px 8px 4px 0", whiteSpace: "nowrap" };
const num: React.CSSProperties = { ...td, textAlign: "right", fontVariantNumeric: "tabular-nums" };

function shortModel(m: string) {
  return m.replace(/^claude-/, "");
}

const COLS_KEY = "covey.cost.modelColumns";

/** The remembered columns of this browser: what was stored, whether or not
 *  this window offers it now (a column the window lacks is remembered, not
 *  shown). The default sources when nothing usable is stored or storage is
 *  not there. */
export function loadColumns(): Source[] {
  try {
    const v: unknown = JSON.parse(window.localStorage.getItem(COLS_KEY) ?? "null");
    if (Array.isArray(v)) {
      const on = SOURCES.filter((c) => v.includes(c));
      if (on.length > 0) return on;
    }
  } catch { /* private mode, or not JSON */ }
  return [...DEFAULT_SOURCES];
}

function saveColumns(cols: Source[]) {
  try { window.localStorage.setItem(COLS_KEY, JSON.stringify(cols)); } catch { /* private mode */ }
}

function ModelTable({ rep, figures, agents, outside = null }: {
  rep: ModelUsageReport; figures: CredentialFigures[]; agents: ReadonlySet<string>;
  /** The sessions outside covey per source and model, included (#120); null: covey only. */
  outside?: { ext: ExternalUsage; rows: OutsideModelRow[]; styles: SourceStyles } | null;
}) {
  const { t } = useTranslation();
  const [chosen, setChosen] = useState<Source[]>(loadColumns);
  const per = useMemo(() => perCredentialTotals(figures), [figures]);
  const perBy: Partial<Record<Source, SourceTotal | null>> = per;
  const filtered = agents.size > 0;
  // The model lines carry their agent, so the per-model columns follow the
  // agent filter like the headline does; the per-credential rows cannot.
  const rows = (rep.rows ?? []).filter((r) => !filtered || agents.has(r.agent_id))
    .sort((a, b) => (b.list_usd ?? b.engine_usd) - (a.list_usd ?? a.engine_usd));
  const list = rows.reduce((a, r) => a + (r.list_usd ?? 0), 0);
  const priced = rows.filter((r) => r.list_usd !== null).reduce((a, r) => a + r.engine_usd, 0);
  const unpricedRows = rows.filter((r) => r.list_usd === null);
  const unpriced = unpricedRows.length;
  // A model the list does not know enters a column that lacks its figure at
  // Claude Code's own price; without it the recomputed total falls short of
  // the headline by exactly that, with nothing saying so.
  const unpricedUSD = unpricedRows.reduce((a, r) => a + r.engine_usd, 0);
  const check = priced > 0 ? ((list - priced) / priced) * 100 : 0;

  // The real cost and the bill are offered only where a meter or a bill exists.
  const offered = SOURCES.filter((c) => isPerModel(c) || perBy[c] != null);
  const shown = offered.filter((c) => chosen.includes(c));
  // What was stored may name only columns this window does not offer: then the default.
  const visible = shown.length > 0 ? shown : offered.filter((c) => DEFAULT_SOURCES.includes(c));
  // Switch within what is remembered, not within what is offered now: a
  // column the window lacks (Real over 24 h, which has no meter) stays in the
  // choice and comes back with the window that offers it.
  const toggle = (c: Source) => {
    const base = shown.length > 0 ? chosen : SOURCES.filter((x) => chosen.includes(x) || visible.includes(x));
    const next = base.includes(c) ? base.filter((x) => x !== c) : SOURCES.filter((x) => x === c || base.includes(x));
    if (!next.some((x) => offered.includes(x))) return; // keep at least one on show
    setChosen(next);
    saveColumns(next);
  };

  const perCredential = t("costs.insights.perCredential");
  const hint = (c: Source) => {
    const h = sourceHint(t, c);
    return isPerModel(c) ? h : `${h} ${perCredential}`;
  };
  // The cell of a per-credential source in its own row, and the reason for
  // it where there is one: none where the credentials with a figure are not
  // all of them. A failed sync is said in either case: a bill it left out is
  // what it explains best.
  // The usage charges (0151) are in none of these figures: beside the real
  // cost, per currency in its own, never added to it or across currencies.
  const usageNote = per.usage ? t("costs.basis.headlineUsage", { list: usageList(t, per.usage) }) : "";
  const perCell = (c: Source, total: SourceTotal) => {
    const failed = total.syncError ? t("costs.fig.billedLastSyncFailed", { error: total.syncError }) : "";
    const usage = c === "real" ? usageNote : "";
    if (total.with < total.of) {
      return { text: "–", why: [t("costs.insights.someWithout", { n: total.of - total.with, m: total.of }), failed, usage].filter(Boolean).join(" · ") };
    }
    if (usage) {
      const inner = perCellFigure(c, total);
      return { text: inner.text, why: [inner.why, usage].filter(Boolean).join(" · ") };
    }
    return perCellFigure(c, total);
  };
  const perCellFigure = (c: Source, total: SourceTotal) => {
    const failed = total.syncError ? t("costs.fig.billedLastSyncFailed", { error: total.syncError }) : "";
    if (total.with === 0) return { text: "–", why: undefined };
    if (SOURCE_INFO[c].estimate) {
      if (total.below) return { text: fmtUSDBelow(total.high), why: undefined };
      // The seats without movement count as 0 in the sum; the whole is
      // somewhere between the lowest and the highest each seat's range allows.
      return { text: `≈ ${fmtUSD(total.usd)}`, why: t("costs.insights.sumRange", { low: fmtUSD(total.low), high: fmtUSD(total.high) }) };
    }
    const why = [total.pending ? t("costs.insights.billedPending") : "", failed].filter(Boolean).join(" ");
    return { text: fmtUSD(total.usd), why: why || undefined };
  };
  // The figures of the per-model sources beside it: the same credentials over the same span.
  const sameScope = (total: SourceTotal, c: Source) => {
    const v = scopeFigure(total, c);
    return total.with === total.of && v != null ? fmtUSD(v) : "–";
  };
  const modelCell = (c: Source, r: ModelUsageRow) => {
    if (!isPerModel(c)) return { text: "–", title: perCredential }; // per credential, not per model
    const v = modelFigure(c, r);
    return { text: v === null ? "–" : fmtUSD(v), title: undefined };
  };
  const modelTotal = (c: Source) => rows.reduce((a, r) => a + (modelFigure(c, r) ?? r.engine_usd), 0);
  const spanOf = (c: SourceTotal) => c.spans.length > 0 ? {
    from: fmtBucket(new Date(Math.min(...c.spans.map((x) => x.from.getTime()))).toISOString(), "hour"),
    to: fmtBucket(new Date(Math.max(...c.spans.map((x) => x.to.getTime()))).toISOString(), "hour"),
  } : { from: "–", to: "–" };
  // Where the row combines several credentials, the span of each: its label
  // names only the combined one.
  const spansOf = (c: SourceTotal) => c.spans.length > 1
    ? c.spans.map((x) => `${x.id}: ${fmtBucket(x.from.toISOString(), "hour")} – ${fmtBucket(x.to.toISOString(), "hour")}`) : [];

  const credentialRows = visible.filter((c) => !isPerModel(c) && perBy[c] != null);
  // The outside sessions: a row per source and model, marked, and a total
  // with them under covey's own (#120). A model with no list price shows
  // its tokens and "no list price", never $0.
  const outRows = outside?.rows ?? [];
  const outCell = (c: Source, r: OutsideModelRow) => {
    if (!isPerModel(c)) return { text: "–", title: perCredential };
    if (c === "reported") return { text: r.reported != null ? fmtUSD(r.reported) : t("costs.incl.noReported"), title: undefined };
    return { text: r.list != null ? fmtUSD(r.list) : t("costs.incl.noList"), title: undefined };
  };
  const outTotal = (c: Source) => outRows.reduce((a, r) => a + ((c === "reported" ? r.reported : r.list) ?? 0), 0);
  // A part with no figure is marked in the total, never added as $0 unsaid (L1).
  const outUnpriced = (c: Source) => outRows.some((r) => (c === "reported" ? r.reported : r.list) == null || (c !== "reported" && r.unpricedTokens > 0));
  if (rows.length === 0 && outRows.length === 0 && credentialRows.length === 0 && offered.every(isPerModel)) return <div className="muted text-sm">{t("costs.empty")}</div>;
  // A column that gives some model line a figure of another source's price.
  const fallsBack = visible.some((c) => isPerModel(c) && rows.some((r) => modelFigure(c, r) === null));

  // What the cells carry in their title stands here in the open as well:
  // a title is not there on a phone or for a keyboard.
  const notes: ReactNode[] = [];
  // The price-table check compares the two per-model sources, so it stands
  // only while both are on.
  if (visible.includes("reported") && visible.includes("recomputed")) {
    notes.push(t("costs.insights.priceTableCheck", { pct: `${check >= 0 ? "+" : ""}${fmtNumber(check, 2)}` }));
  }
  if (unpriced > 0) notes.push(t("costs.insights.unpriced", { count: unpriced }));
  if (outRows.length > 0) {
    notes.push(t("costs.incl.modelsNote"));
    const unpricedOut = outRows.reduce((a, r) => a + r.unpricedTokens, 0);
    if (unpricedOut > 0) notes.push(t("costs.incl.modelsUnpriced", { n: fmtCount(unpricedOut) }));
  }
  if (rep.runs_without_breakdown > 0) {
    // Runs without a model record have no agent here: the figure is the whole organisation's.
    notes.push(<>{t("costs.insights.without", { count: rep.runs_without_breakdown, usd: fmtUSD(rep.usd_without_breakdown) })}{filtered && <OrgWide />}</>);
  }
  for (const c of visible.filter((x) => !isPerModel(x))) {
    const total = perBy[c];
    notes.push([sourceLabel(t, c) + ":", perCredential, total ? perCell(c, total).why : undefined,
      total ? spansOf(total).join("; ") : undefined].filter(Boolean).join(" "));
  }
  // Where no real-cost row carries it, the usage charges stand on their own.
  if (usageNote && !(visible.includes("real") && perBy.real)) notes.push(<span data-usage-note>{usageNote}</span>);
  return (
    <div>
      <div className="flex flex-wrap items-center gap-2" style={{ marginBottom: 8 }} role="group" aria-label={t("costs.filter.source")}>
        <span className="muted text-xs" style={{ marginRight: 4 }}>{t("costs.filter.source")}</span>
        {offered.map((c) => {
          const locked = visible.length === 1 && visible[0] === c;
          return (
            <Chip key={c} on={visible.includes(c)} color={SOURCE_COLOR[c]} hatched={SOURCE_INFO[c].estimate} onClick={() => toggle(c)}
              locked={locked} title={locked ? t("costs.insights.lastColumn") : undefined}>
              {sourceLabel(t, c)}
            </Chip>
          );
        })}
      </div>
      <div style={{ overflowX: "auto" }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
          <thead>
            <tr className="muted" style={{ textAlign: "left" }}>
              <th style={th}>{t("costs.insights.agent")}</th>
              <th style={th}>{t("costs.insights.model")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.runs")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.output")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.cacheRead")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.cacheWrite")}</th>
              {visible.map((c) => (
                <th key={c} data-col={c} style={{ ...th, textAlign: "right" }} title={hint(c)}>{t(`costs.insights.col_${c}`)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && outRows.length === 0 && (
              <tr data-empty style={{ borderTop: "1px solid var(--border)" }}>
                <td style={td} colSpan={6 + visible.length} className="muted">{t("costs.empty")}</td>
              </tr>
            )}
            {rows.map((r) => (
              <tr key={`${r.agent_id}/${r.model}/${r.credential_kind}`} style={{ borderTop: "1px solid var(--border)" }}>
                <td style={td}>
                  <Link to={`/agents/${r.agent_id}`}>{r.agent_name || r.agent_slug}</Link>
                </td>
                <td style={td}>{shortModel(r.model)}</td>
                <td style={num}>{r.runs}</td>
                <td style={num} title={t("costs.insights.thinkingOf", { n: fmtCount(r.thinking_tokens) })}>
                  {fmtCount(r.output_tokens)}
                </td>
                <td style={num}>{fmtCount(r.cache_read_tokens)}</td>
                <td style={num} title={`5m ${fmtCount(r.cache_write_5m_tokens)} · 1h ${fmtCount(r.cache_write_1h_tokens)}`}>
                  {fmtCount(r.cache_write_5m_tokens + r.cache_write_1h_tokens)}
                </td>
                {visible.map((c) => {
                  const x = modelCell(c, r);
                  return <td key={c} style={num} title={x.title}>{x.text}</td>;
                })}
              </tr>
            ))}
            {unpriced > 0 && (
              <tr data-unpriced style={{ borderTop: "1px solid var(--border)" }}>
                <td style={{ ...td, whiteSpace: "normal" }} colSpan={6} className="muted">{t("costs.insights.unpricedRow", { count: unpriced })}</td>
                {visible.map((c) => (
                  <td key={c} style={num}>{isPerModel(c) ? fmtUSD(unpricedUSD) : ""}</td>
                ))}
              </tr>
            )}
            <tr data-total style={{ borderTop: "1px solid var(--border)", fontWeight: 600 }}>
              <td style={td} colSpan={6}>
                {outRows.length > 0
                  ? t(filtered ? "costs.incl.subtotalSelected" : "costs.incl.subtotal")
                  : t(fallsBack ? "costs.insights.totalWithUnpriced" : "costs.insights.total")}
              </td>
              {/* the real cost and the bill have rows of their own */}
              {visible.map((c) => <td key={c} data-col={c} style={num}>{isPerModel(c) ? fmtUSD(modelTotal(c)) : ""}</td>)}
            </tr>
            {outRows.map((r) => (
              <tr key={`out/${r.source}/${r.model}`} data-outside-row={r.source} style={{ borderTop: "1px solid var(--border)" }}>
                <td style={td}>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                    <Swatch color={SOURCE_COLOR.recomputed} {...outside!.styles.style(r.source)} /> {r.source}
                  </span>
                  <OutsideMark />
                </td>
                <td style={td}>{shortModel(r.model)}</td>
                <td style={num} title={t("costs.incl.callsTitle", { n: fmtNumber(r.calls, 0) })}>{fmtNumber(r.calls, 0)}</td>
                <td style={num}>{fmtCount(r.output)}</td>
                <td style={num}>{fmtCount(r.cacheRead)}</td>
                <td style={num} title={`5m ${fmtCount(r.cacheWrite5m)} · 1h ${fmtCount(r.cacheWrite1h)}`}>{fmtCount(r.cacheWrite5m + r.cacheWrite1h)}</td>
                {visible.map((c) => {
                  const x = outCell(c, r);
                  return <td key={c} data-col={c} style={num} title={x.title}>{x.text}</td>;
                })}
              </tr>
            ))}
            {outRows.length > 0 && (
              <tr data-incl-total style={{ borderTop: "1px solid var(--border-strong)", fontWeight: 600 }}>
                <td style={td} colSpan={6}>{t(filtered ? "costs.incl.totalSelected" : "costs.incl.total")}</td>
                {visible.map((c) => <td key={c} data-col={c} style={num}>{isPerModel(c) ? withUnpriced(t, modelTotal(c) + outTotal(c), outUnpriced(c)) : ""}</td>)}
              </tr>
            )}
            {credentialRows.map((c) => {
              const total = perBy[c]!;
              return (
                <tr key={c} data-credential-row={c} style={{ borderTop: "1px solid var(--border)" }}>
                  <td style={{ ...td, whiteSpace: "normal" }} colSpan={6} title={spansOf(total).join("\n") || undefined}>
                    {t(SOURCE_INFO[c].rowLabel!, spanOf(total))}{filtered && <OrgWide />}
                  </td>
                  {visible.map((x) => {
                    const own = x === c ? perCell(c, total) : { text: isPerModel(x) ? sameScope(total, x) : "", why: undefined };
                    return <td key={x} data-col={x} style={num} title={own.why}>{own.text}</td>;
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="muted text-xs mt-2 mb-0">
        {notes.map((n, i) => <Fragment key={i}>{i > 0 && " · "}{n}</Fragment>)}
      </p>
    </div>
  );
}

/** One credential's meter with the figures of its tiles above: the span the
 *  meter was read over, the real cost of its movement (or why there is
 *  none), the list price of the credential's lines over the same span, and
 *  the multiple. No chart: two independent scales stretched to the same
 *  height agreed by construction, not by the data. */
function MeterFigures({ f, readings }: { f: CredentialFigures; readings: UsageReading[] }) {
  const { t } = useTranslation();
  const m = f.meter!;
  const r = f.real;
  const source = t(`costs.insights.source_${m.source}`, { defaultValue: m.source });
  const own = readings.filter((x) => x.secret_key === m.secret_key && x.secret_slot === m.secret_slot);
  const sameWindow = own.filter((x) => x.source === m.source && (x.window_kind === "seven_day" || x.window_kind === ""));
  const dropped = sameWindow.length - usableReadings(own, m.secret_key, m.secret_slot, m.source).length;
  const five = own.filter((x) => x.supported && !x.stale && x.window_percent != null).at(-1);

  let real: string;
  // Billed by usage all through (0151): the meter still moves, but it is not
  // the money. A seat that changed basis keeps its seat part, said as such.
  const seatPart = r && r.usagePoints > 0 ? ` · ${t("costs.basis.realSeatPart", { n: fmtNumber(r.usagePoints, 0) })}` : "";
  if (!r && f.usage?.regimes.length === 1 && f.usage.regimes[0] === "usage") real = t("costs.basis.meterNotMoney");
  else if (!r) real = t("costs.fig.noMeter");
  else if (!r.priced) real = r.unpricedPoints > 0 ? t("costs.fig.noPlanPricePoints", { n: r.unpricedPoints, m: r.points }) : t("costs.fig.noPlan");
  else if (r.below) real = `${fmtUSDBelow(r.high)} · ${t("costs.fig.realBelow")}${seatPart}`;
  else real = `≈ ${fmtUSD(r.value)} · ${t("costs.fig.realRange", { low: fmtUSD(r.low), high: fmtUSD(r.high), pts: r.points, pp: fmtUSDFixed(r.value / r.points, 4) })}${seatPart}`;

  let mult: string;
  // On a seat billed by usage (0151) no multiple of the meter stands for
  // everything on it: the usage multiple's rules below do.
  const e = f.everything?.why === "usage" ? null : f.everything;
  if (e) {
    // Included (#120): the multiple of everything on the meter.
    mult = e.multiple && e.estimate != null
      ? `≈ ${times(e.estimate)} · ${t("costs.incl.everythingFloor", { floor: times(e.multiple.low) })} · ${t("costs.incl.everythingShort")}`
      : e.why === "movement" ? t("costs.incl.everythingMovement", { pts: fmtNumber(e.points, 0) })
      : e.why === "unpriced" ? t("costs.incl.everythingUnpriced", { n: fmtNumber(e.unpricedPoints, 0) })
      : t("costs.incl.everythingNoList");
  } else if (f.multiple?.kind === "estimate") {
    mult = `≈ ${times(f.multiple.value)} · ${t("costs.fig.multipleRange", { low: times(f.multiple.low), high: times(f.multiple.high) })}`;
  } else if (f.noMultiple === "unpriced") {
    mult = r && r.unpricedPoints > 0 ? t("costs.fig.noMultipleUnpriced", { n: r.unpricedPoints, m: r.points }) : t("costs.fig.noMultipleNoPlan");
  } else if (f.noMultiple === "movement" && r && r.points >= 1 && f.aligned) {
    mult = t("costs.fig.notEnoughMovement", { pts: r.points, low: times(f.aligned / r.high), high: times(r.low > 0 ? f.aligned / r.low : Infinity) });
  } else if (f.noMultiple === "movement") {
    mult = t("costs.fig.notEnoughMovementNone");
  } else if (f.multiple?.kind === "floor") {
    mult = `≥ ${times(f.multiple.low)} · ${t("costs.split.multipleFloor")}`;
  } else if (f.noMultiple === "outside") {
    mult = t("costs.split.noMultipleOutside");
  } else if (f.usageMultiple != null) {
    mult = `${f.usageShared ? "≥ " : ""}${times(f.usageMultiple)} · ${t("costs.basis.usageMultipleSub", { list: fmtUSD(f.usageList ?? 0) })}`
      + (f.usageShared ? ` · ${t("costs.basis.usageMultipleShared", { sources: f.usageShared.join(", ") })}` : "");
  } else if (f.noMultiple === "rate") {
    mult = t("costs.basis.noMultipleRate");
  } else if (f.noMultiple === "noCharge") {
    mult = t("costs.basis.noMultipleNoCharge");
  } else if (f.noMultiple === "spanList") {
    mult = t("costs.basis.noMultipleSpanList");
  } else if (f.noMultiple === "basisChanged") {
    mult = t("costs.basis.noMultipleChanged");
  } else if (f.noMultiple === "seatAndUsage") {
    mult = t("costs.basis.noMultipleBoth");
  } else mult = "–";

  const row = (label: string, value: string, color?: string, hatched?: boolean) => (
    <div style={{ display: "flex", gap: 8, alignItems: "baseline", padding: "2px 0" }}>
      <span className="muted text-xs" style={{ display: "inline-flex", alignItems: "center", gap: 6, width: 190, flexShrink: 0 }}>
        {color && <Swatch color={color} hatched={hatched} />}{label}
      </span>
      <span className="text-sm" style={{ fontVariantNumeric: "tabular-nums" }}>{value}</span>
    </div>
  );
  return (
    <div className="mb-4" data-meter={f.id}>
      <div className="text-xs" style={{ fontWeight: 600, marginBottom: 4 }}>{`${m.secret_key} #${m.secret_slot} · ${source}`}</div>
      {row(t("costs.insights.meterSpan"), t("costs.insights.meterSpanValue", {
        from: m.baseline_percent, fromAt: fmtBucket(m.baseline_at, "hour"), to: m.last_percent, toAt: fmtBucket(m.last_at, "hour"),
        pts: m.points, n: m.readings,
      }))}
      {row(t("costs.fig.realEstimate"), real, SOURCE_COLOR.real, true)}
      {row(t("costs.insights.meterList"), f.aligned != null ? fmtUSD(f.aligned) : "–", SOURCE_COLOR.recomputed)}
      {e && row(t("costs.incl.meterListOutside"), withinMoney(t, e.outsideList,
        { priced: e.outsidePriced, calls: e.outsideCalls, unpricedTokens: e.outsideUnpricedTokens }), SOURCE_COLOR.recomputed)}
      {row(e ? t("costs.incl.everything") : t("costs.fig.multiple"), mult)}
      <p className="muted text-xs mt-1 mb-0">
        {five
          ? t("costs.insights.fiveHour", { pct: five.window_percent, at: fmtBucket(five.fetched_at, "hour") })
          : t("costs.insights.fiveHourNotReported")}
        {dropped > 0 && <> · {t("costs.insights.dropped", { count: dropped })}</>}
      </p>
    </div>
  );
}

const NO_AGENTS: ReadonlySet<string> = new Set();
const times = (v: number) => (Number.isFinite(v) ? `${fmtNumber(v, 1)}×` : "∞");

/** The cards below the chart. `figures` are the credentials' figures over the
 *  whole organisation — the same the tiles show without an agent filter —
 *  while the model table follows `agents`, the page's agent filter (empty:
 *  all agents). */
export function CostInsights({ days, figures, agents = NO_AGENTS, outside = null }: {
  days: number; figures: CredentialFigures[]; agents?: ReadonlySet<string>;
  /** The sessions outside covey, included (#120); null: covey only. */
  outside?: ExternalUsage | null;
}) {
  const { t } = useTranslation();
  const models = useQuery({
    queryKey: ["cost", "models", days],
    queryFn: () => api<ModelUsageReport>(`/cost/models?days=${days}`),
    refetchInterval: 30000,
  });
  // Per source and model, from the outside sessions' own read (the agents'
  // /cost/models stays theirs alone).
  // Fetched again only when an outside call landed (the sources in
  // /cost/cells, polled anyway, changed), not every 30 s (L4).
  const stamp = useMemo(() => externalStamp(outside), [outside]);
  const outsideModels = useQuery({
    queryKey: ["cost", "external-usage", days, "day", null, stamp],
    queryFn: () => api<ExternalModelUsage>(`/cost/external-usage?days=${days}&bucket=day`),
    staleTime: Infinity,
    placeholderData: (prev) => prev,
    enabled: outside != null,
  });
  const styles = useMemo(() => sourceStyles(outside), [outside]);
  const outsideRows = useMemo(() => {
    if (!outside || !outsideModels.data) return null;
    const names = new Set(listedSources(outside).map((s) => s.source));
    return { ext: outside, styles, rows: outsideModelRows(outsideModels.data.cells.filter((c) => names.has(c.source))) };
  }, [outside, outsideModels.data, styles]);
  const readings = useUsageReadings(days);
  const rs = useMemo(() => readings.data ?? [], [readings.data]);
  const filtered = agents.size > 0;
  const metered = figures.filter((f) => f.meter != null && isSeat(f));

  return (
    <div className="grid gap-4 mt-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(420px, 100%), 1fr))" }}>
      <div className="card" style={{ gridColumn: "1 / -1" }}>
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.modelsTitle")}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.modelsHint")}</p>
        {models.data ? <ModelTable rep={models.data} figures={figures} agents={agents} outside={outsideRows} /> : <div className="muted text-sm">…</div>}
      </div>
      <div className="card">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.meterTitle")}{filtered && <OrgWide />}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.meterHint")}</p>
        {metered.length === 0 && <div className="muted text-sm">{t("costs.insights.noReadings")}</div>}
        {metered.map((f) => <MeterFigures key={f.id} f={f} readings={rs} />)}
        <p className="muted text-xs mt-2 mb-0">{t("costs.fig.accountWideCaveat")}</p>
      </div>
    </div>
  );
}
