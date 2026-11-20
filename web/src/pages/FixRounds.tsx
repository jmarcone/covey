import { useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { api, type FixFigure, type FixRoundGroup, type FixRoundReport } from "../api";
import { fmtNumber, fmtUSD } from "../format";
import { Seg } from "./Costs";

/* Fix rounds (sticky sessions, #130 AC 8/9).
 *
 * Whether resuming a developer's session on the same pull request pays: per
 * dispatch decision (resumed against fresh) and per policy (the A/B test, read
 * per agent, never across models), what the first turn wrote to the cache
 * (split 5 min / 1 h: a 5-minute write inside a 55-minute window is a full
 * rewrite) and read from it, and what a round cost. Every figure carries n;
 * n = 0 is "no data", never 0. The window is a week of its own — the figure is
 * read week by week, whatever range the page above shows. */

const DAY = 24 * 60 * 60 * 1000;
/** The weeks offered: this one and the three before it. */
export const FIX_WEEKS = [0, 1, 2, 3] as const;

/** The window of the week `back` weeks before `now`: [now - 7(back+1) days, now - 7·back days). */
export function fixWindow(now: Date, back: number): { from: Date; to: Date } {
  const to = new Date(now.getTime() - back * 7 * DAY);
  return { from: new Date(to.getTime() - 7 * DAY), to };
}

const th: CSSProperties = { padding: "4px 8px 8px 0", fontWeight: 500, whiteSpace: "nowrap", textAlign: "right" };
const thl: CSSProperties = { ...th, textAlign: "left" };
const td: CSSProperties = { padding: "4px 8px 4px 0", whiteSpace: "nowrap" };
const num: CSSProperties = { ...td, textAlign: "right", fontVariantNumeric: "tabular-nums" };

/** One figure with its n beside it, or "no data". */
export function FigureCell({ f, fmt }: { f: FixFigure; fmt: (v: number) => string }) {
  const { t } = useTranslation();
  if (f.n === 0 || f.mean == null) {
    return <td style={num} className="muted" data-no-data>{t("costs.fixRounds.noData")}</td>;
  }
  return (
    <td style={num}>
      {fmt(f.mean)} <span className="muted text-xs">n={f.n}</span>
    </td>
  );
}

const tokens = (v: number) => fmtNumber(v, 0);
const minutes = (v: number) => fmtNumber(v, 0);

function GroupHead({ first, t }: { first: ReactNode; t: TFunction }) {
  return (
    <tr className="muted text-xs">
      {first}
      <th style={th}>{t("costs.fixRounds.colRounds")}</th>
      <th style={th}>{t("costs.fixRounds.colUsd")}</th>
      <th style={th}>{t("costs.fixRounds.colWrite")}</th>
      <th style={th}>{t("costs.fixRounds.colWrite5m")}</th>
      <th style={th}>{t("costs.fixRounds.colWrite1h")}</th>
      <th style={th}>{t("costs.fixRounds.colRead")}</th>
      <th style={th}>{t("costs.fixRounds.colGap")}</th>
      <th style={th}>{t("costs.fixRounds.colContext")}</th>
    </tr>
  );
}

function GroupCells({ g }: { g: FixRoundGroup }) {
  return (
    <>
      <td style={num}>{fmtNumber(g.rounds, 0)}</td>
      <FigureCell f={g.usd} fmt={fmtUSD} />
      <FigureCell f={g.first_turn_cache_write} fmt={tokens} />
      <FigureCell f={g.first_turn_cache_write_5m} fmt={tokens} />
      <FigureCell f={g.first_turn_cache_write_1h} fmt={tokens} />
      <FigureCell f={g.first_turn_cache_read} fmt={tokens} />
      <FigureCell f={g.gap_minutes} fmt={minutes} />
      <FigureCell f={g.context_tokens} fmt={tokens} />
    </>
  );
}

const policyLabel = (t: TFunction, p: string) => t(`costs.fixRounds.policy.${p}`, { defaultValue: p });

export function FixRoundsView({ rep }: { rep: FixRoundReport }) {
  const { t } = useTranslation();
  const arms = rep.agents.flatMap((a) => a.by_policy.filter((g) => g.rounds > 0).map((g) => ({ slug: a.slug, id: a.agent_id, g })));
  const total = rep.by_decision.reduce((s, g) => s + g.rounds, 0);
  return (
    <>
      <div className="text-sm font-medium mt-2 mb-1">{t("costs.fixRounds.byDecision")}</div>
      {total === 0 && <p className="muted text-xs" data-fix-none>{t("costs.fixRounds.none")}</p>}
      <div style={{ overflowX: "auto" }}>
        <table className="text-sm" style={{ borderCollapse: "collapse", width: "100%" }} data-fix-decision>
          <thead><GroupHead t={t} first={<th style={thl}>{t("costs.fixRounds.colDecision")}</th>} /></thead>
          <tbody>
            {rep.by_decision.map((g) => (
              <tr key={g.key} data-key={g.key}>
                <td style={td}>{t(`costs.fixRounds.decision.${g.key}`, { defaultValue: g.key })}</td>
                <GroupCells g={g} />
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="text-sm font-medium mt-4 mb-1">{t("costs.fixRounds.byPolicy")}</div>
      {arms.length === 0 ? (
        <p className="muted text-xs">{t("costs.fixRounds.noData")}</p>
      ) : (
        <div style={{ overflowX: "auto" }}>
          <table className="text-sm" style={{ borderCollapse: "collapse", width: "100%" }} data-fix-policy>
            <thead><GroupHead t={t} first={<><th style={thl}>{t("costs.fixRounds.colAgent")}</th><th style={thl}>{t("costs.fixRounds.colPolicy")}</th></>} /></thead>
            <tbody>
              {arms.map(({ slug, id, g }) => (
                <tr key={`${id}:${g.key}`}>
                  <td style={td} className="mono">{slug}</td>
                  <td style={td}>{policyLabel(t, g.key)}</td>
                  <GroupCells g={g} />
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="text-sm font-medium mt-4 mb-1">{t("costs.fixRounds.perPR")}</div>
      {rep.rounds_per_pr.length === 0 ? (
        <p className="muted text-xs">{t("costs.fixRounds.noData")}</p>
      ) : (
        <div style={{ overflowX: "auto" }}>
          <table className="text-sm" style={{ borderCollapse: "collapse" }} data-fix-prs>
            <thead>
              <tr className="muted text-xs">
                <th style={thl}>{t("costs.fixRounds.colAgent")}</th>
                <th style={thl}>{t("costs.fixRounds.colPolicy")}</th>
                <th style={th}>{t("costs.fixRounds.colPRs")}</th>
                <th style={th}>{t("costs.fixRounds.colPerPR")}</th>
              </tr>
            </thead>
            <tbody>
              {rep.rounds_per_pr.map((r) => (
                <tr key={`${r.agent_id}:${r.policy}`}>
                  <td style={td} className="mono">{r.slug}</td>
                  <td style={td}>{policyLabel(t, r.policy)}</td>
                  <td style={num}>{fmtNumber(r.prs, 0)}</td>
                  <FigureCell f={r.rounds} fmt={(v) => fmtNumber(v, 1)} />
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

export function FixRounds() {
  const { t, i18n } = useTranslation();
  // One instant for the page's life: the windows do not drift while it is open.
  const now = useMemo(() => new Date(), []);
  const [back, setBack] = useState<number>(0);
  const { from, to } = fixWindow(now, back);
  const q = useQuery({
    queryKey: ["cost", "fix-rounds", from.toISOString(), to.toISOString()],
    queryFn: () =>
      api<FixRoundReport>(
        `/cost/fix-rounds?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`,
      ),
  });
  const day = (d: Date) => d.toLocaleDateString(i18n.language, { month: "short", day: "numeric" });
  const options = FIX_WEEKS.map((b) => {
    const w = fixWindow(now, b);
    return { v: b as number, label: `${day(w.from)} – ${day(w.to)}` };
  });
  return (
    <div className="card mb-4" data-fix-rounds>
      <div className="flex items-center justify-between flex-wrap gap-2 mb-1">
        <div style={{ fontWeight: 600 }}>{t("costs.fixRounds.title")}</div>
        <Seg value={back} onChange={setBack} options={options} />
      </div>
      <p className="muted text-xs mb-2" style={{ maxWidth: 760 }}>{t("costs.fixRounds.hint")}</p>
      {q.isError ? (
        <p className="muted text-xs">{t("costs.fixRounds.error")}</p>
      ) : q.data ? (
        <FixRoundsView rep={q.data} />
      ) : null}
    </div>
  );
}
