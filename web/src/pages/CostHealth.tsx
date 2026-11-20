import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type CostHealthCheck, type CostHealthReport, type CostHealthStatus } from "../api";
import { fmtDelta, fmtNumber } from "../format";
import { uiLocale } from "../i18n";
import { counted } from "./OutsideSessions";

/* Cost health (#178): whether the figures on the cost page can be trusted
 * right now. The server runs the checks (GET /cost/health, kept five minutes)
 * and decides the worst status; this file words them in the reader's
 * language, shows them on /costs/health, and gives the cost page its badge
 * and the "incomplete" marks. A red check never makes a figure smaller or
 * hides it: the part it affects is marked, and the figure stays. When the
 * checks cannot run, nothing here is green: a result from before stays on
 * /costs/health with its time, and the badge says "unavailable". */

export const HEALTH_PATH = "/costs/health";

export function useCostHealth() {
  return useQuery({
    queryKey: ["cost", "health"],
    queryFn: () => api<CostHealthReport>("/cost/health"),
    // The server keeps a report five minutes: asking more often buys nothing.
    refetchInterval: 300_000,
  });
}

/** The check that gives the report its status — the first one with it, as
 *  the server's HealthReport.Worst; null when nothing warns or fails. */
export function worstCheck(rep: CostHealthReport | undefined): CostHealthCheck | null {
  if (!rep || (rep.status !== "warn" && rep.status !== "fail")) return null;
  return rep.checks.find((c) => c.status === rep.status) ?? null;
}

export type IncompletePart = "outside" | "multiple" | "tokenPrice";

/** Which calls a page's token-price figures hold: covey's agents' lines,
 *  the outside sessions', or both. */
export type TokenScope = { agents: boolean; outside: boolean };

const failedCheck = (rep: CostHealthReport | undefined, id: string) =>
  rep?.checks.find((c) => c.id === id && c.status === "fail") ?? null;
const only = (...cs: (CostHealthCheck | null | undefined | false)[]) => cs.filter((c): c is CostHealthCheck => !!c);

/** The parts of the cost page a failed check leaves incomplete, each with
 *  every check that does (none: an empty list): a stopped uploader (c1)
 *  the outside sessions, wherever they are counted (the headline too); a
 *  reset nobody recorded (c3) or a seat that spent with no meter reading
 *  (c2) the multiple; a call booked twice (c4, larger) or a model without a
 *  list price (c5, smaller) every figure at token prices — the tiles, the
 *  chart, the model table and the headline's list price. Those two count
 *  only where the page holds the calls they concern (`tokens`): "covey
 *  only" is not marked for a model only a laptop used, nor /costs/outside
 *  for an agent line booked twice. */
export function incompleteParts(rep: CostHealthReport | undefined, tokens: TokenScope = { agents: true, outside: true }): Record<IncompletePart, CostHealthCheck[]> {
  const c4 = failedCheck(rep, "c4"), c5 = failedCheck(rep, "c5");
  const f4 = c4?.facts, f5 = c5?.facts;
  return {
    outside: only(failedCheck(rep, "c1")),
    multiple: only(failedCheck(rep, "c3"), failedCheck(rep, "c2")),
    tokenPrice: only(
      (tokens.agents && (f4?.agent_duplicates ?? 0) > 0 || tokens.outside && (f4?.external_duplicates ?? 0) > 0) && c4,
      (tokens.agents && (f5?.unpriced_agents?.length ?? 0) > 0 || tokens.outside && (f5?.unpriced_outside?.length ?? 0) > 0) && c5),
  };
}

/** The checks that leave one credential's own figures short (secret #slot,
 *  as the server names it): a reset nobody recorded on its meter (c3 names
 *  the credentials) or no reading where it spent (c2 names the seat) its
 *  real cost and its multiples; a stopped uploader of a source on its seat
 *  (c1) the outside part of them — only where the outside sessions are
 *  included (`sources`: the seat's; null: covey only). */
export function credentialIncomplete(rep: CostHealthReport | undefined, credential: string, sources: readonly string[] | null):
  { multiple: CostHealthCheck[]; outside: CostHealthCheck[] } {
  const c3 = failedCheck(rep, "c3"), c2 = failedCheck(rep, "c2"), c1 = failedCheck(rep, "c1");
  return {
    multiple: only(c3?.facts.credentials?.includes(credential) && c3, c2?.facts.credential === credential && c2),
    outside: only(sources != null && c1?.facts.stale_hosts?.some((h) => sources.includes(h.source)) && c1),
  };
}

const ago = (minutes: number | undefined) => (minutes == null ? "–" : fmtDelta(minutes * 60_000));
const when = (iso: string | undefined) => (iso ? new Date(iso).toLocaleString(uiLocale()) : "–");

type Host = NonNullable<CostHealthCheck["facts"]["stale_hosts"]>[number];
const hostList = (hs: Host[] | undefined) => (hs ?? []).map((h) => `${h.source} @ ${h.host} (${ago(h.minutes)})`).join(", ");

/** The check's reason as a sentence. */
export function reasonText(t: TFunction, c: CostHealthCheck): string {
  const f = c.facts;
  const key = `costs.health.reason.${c.id}_${c.reason}`;
  // The names of the controls and bases a reason points at, as the page shows them.
  const usage = t("costs.basis.kind_usage");
  switch (`${c.id}_${c.reason}`) {
    case "c1_fresh":
    case "c1_all_silent":
    case "c1_gone":
      return t(key, { ago: ago(f.last_push_minutes) });
    case "c1_stale":
      return t(key, { hosts: hostList(f.stale_hosts), ago: ago(f.last_push_minutes) });
    case "c1_quiet":
      return t(key, { hosts: hostList(f.quiet_hosts) });
    case "c2_no_meter":
      return t(key, { usage });
    case "c2_none":
    case "c2_fresh":
    case "c2_stale":
      return t(c.reason === "stale" && f.fleet_killed ? `${key}Paused` : key, {
        credential: f.credential ?? "–", ago: ago(f.reading_minutes), action: t("costs.basis.addReading"),
        pct: f.week_percent != null ? `${fmtNumber(f.week_percent, 0)} %` : "–",
      });
    case "c3_drops":
    case "c3_early_reset":
      return t(key, { n: (c.reason === "drops" ? f.drops : f.early_resets) ?? 0, when: when(f.last_drop_at),
        credentials: (f.credentials ?? []).join(", ") || "–" });
    case "c4_duplicates":
      return t(key, { ...counted((f.external_duplicates ?? 0) + (f.agent_duplicates ?? 0)),
        outside: fmtNumber(f.external_duplicates ?? 0, 0), agents: fmtNumber(f.agent_duplicates ?? 0, 0) });
    case "c5_priced":
      return t(key, counted(f.models ?? 0));
    case "c5_unpriced":
      // A line with tokens and no model name is listed as "" by the server.
      return t(key, { models: (f.unpriced ?? []).map((m) => m || t("costs.health.unnamedModel")).join(", ") });
    case "c6_set":
      return t(key, { usage });
    case "c6_missing":
      return t(key, { usage, credentials: (f.credentials ?? []).join(", ") });
    case "c6_unattributed":
      return t(key, counted(f.unattributed ?? 0));
    case "c7_ok":
    case "c7_failed":
    case "c7_stopped":
    case "c7_not_landed":
      return t(key, {
        ago: f.last_success_at ? fmtDelta(Date.parse(c.checked_at) - Date.parse(f.last_success_at)) : "–",
        attempt: ago(f.last_attempt_minutes),
      });
    case "c8_pre_meter":
      return t(key, counted(f.calls ?? 0));
    default:
      // A reason this build does not know (a newer server): the code, never nothing.
      return t(key, { defaultValue: c.reason });
  }
}

const PILL: Record<CostHealthStatus, string> = { pass: "ok", warn: "warn", fail: "err", info: "mut" };

export function StatusPill({ status }: { status: CostHealthStatus }) {
  const { t } = useTranslation();
  return <span className={`pill ${PILL[status]}`} data-health-status={status}>{t(`costs.health.status.${status}`)}</span>;
}

/** The badge beside the cost page's headline: the worst status and its
 *  reason, linking to /costs/health. While the checks have not answered
 *  there is no badge; when they could not run, it says so — never green,
 *  also when an earlier answer is still at hand (react-query keeps it after
 *  a failed refetch): that one is named with its time, without its colour. */
export function CostHealthBadge() {
  const { t } = useTranslation();
  const q = useCostHealth();
  if (q.isError) {
    const last = q.data && t("costs.health.lastResult", { when: when(q.data.checked_at), status: t(`costs.health.status.${q.data.status}`) });
    return (
      <Link to={HEALTH_PATH} className="text-xs" data-health-badge="error"
        aria-label={last ? `${t("costs.health.unavailable")}. ${last}` : undefined}
        style={{ display: "inline-flex", alignItems: "center", gap: 6, minWidth: 0, maxWidth: 520 }}>
        <span className="pill mut">{t("costs.health.unavailable")}</span>
        {last && <span className="muted" data-health-last style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{last}</span>}
      </Link>
    );
  }
  if (!q.data) return null;
  const w = worstCheck(q.data);
  const reason = w ? `${t(`costs.health.name.${w.id}`)}: ${reasonText(t, w)}` : t("costs.health.allPassed");
  return (
    <Link to={HEALTH_PATH} className="text-xs" data-health-badge={q.data.status} title={reason}
      aria-label={t("costs.health.badgeLabel", { status: t(`costs.health.status.${q.data.status}`), reason })}
      style={{ display: "inline-flex", alignItems: "center", gap: 6, minWidth: 0, maxWidth: 520 }}>
      <StatusPill status={q.data.status} />
      <span className="muted" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{reason}</span>
    </Link>
  );
}

/** "incomplete" beside a part failed checks affect; their reasons on hover
 *  and the checks one click away. Nothing without a check. */
export function IncompleteMark({ checks, part }: { checks: readonly CostHealthCheck[]; part: IncompletePart }) {
  const { t } = useTranslation();
  if (checks.length === 0) return null;
  const reason = checks.map((c) => `${t(`costs.health.name.${c.id}`)}: ${reasonText(t, c)}`).join(" · ");
  return (
    <Link to={HEALTH_PATH} className="pill err" data-incomplete={part} title={reason}
      aria-label={t(`costs.health.incomplete.${part}`, { reason })} style={{ fontWeight: 400 }}>
      {t("costs.health.incomplete.mark")}
    </Link>
  );
}

/** /costs/health: every check with its status and reason, and when they ran. */
export default function CostHealthPage() {
  const { t } = useTranslation();
  const q = useCostHealth();
  return (
    <div>
      <div className="mb-1"><Link to="/costs" className="text-sm">← {t("costs.outside.back")}</Link></div>
      <div className="flex items-baseline gap-3 mb-1">
        <h1 className="text-[22px]">{t("costs.health.title")}</h1>
        {q.data && !q.isError && <StatusPill status={q.data.status} />}
      </div>
      <p className="muted text-sm mb-4" style={{ maxWidth: 680 }}>{t("costs.health.hint")}</p>
      {q.isError && <p className="text-sm mb-3" role="alert" data-health-error>
        {q.data ? t("costs.health.loadErrorStale", { when: when(q.data.checked_at) }) : t("costs.health.loadError")}</p>}
      {q.data && <>
        <p className="muted text-xs mb-3" data-health-checked>{t("costs.health.checkedAt", { when: when(q.data.checked_at) })}</p>
        <div className="card">
          <table className="w-full text-sm" data-health-table>
            <tbody>
              {q.data.checks.map((c) => (
                <tr key={c.id} data-health-check={c.id} style={{ verticalAlign: "top" }}>
                  <td style={{ padding: "6px 8px", width: 90 }}><StatusPill status={c.status} /></td>
                  <td style={{ padding: "6px 8px", width: 220, fontWeight: 600 }}>{t(`costs.health.name.${c.id}`)}</td>
                  <td style={{ padding: "6px 8px" }}>
                    {reasonText(t, c)}
                    {c.facts.last_error && <div className="mt-1"><code className="text-xs">{c.facts.last_error}</code></div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </>}
    </div>
  );
}
