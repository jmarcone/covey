import { Component, Suspense, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api";

/* Waiting on the cost pages: cost/cells takes seconds over the default range, so every block says for itself whether
 * it is loading, could not be loaded, or has its answer. "No cost data" and $0.00 are answers, and stand only once
 * the query has answered. A screen reader hears it once per view (CostStatus), not per block. Imported by the app's shell too: nothing of
 * the cost views may be imported here. */

export type Load = "ready" | "loading" | "error";

/** Where one query stands for the key on screen. An answer stays through a refetch. A failure stays one while the
 *  interval tries again (react-query puts a query without data back to pending then), and a request that cannot go
 *  out (offline) is no endless wait either. */
export function loadState(q: { data: unknown; isError: boolean; isPaused: boolean; errorUpdateCount: number; isFetchedAfterMount: boolean }): Load {
  if (q.data !== undefined) return "ready";
  return q.isError || q.isPaused || (q.errorUpdateCount > 0 && q.isFetchedAfterMount) ? "error" : "loading";
}

/** A block that needs several answers: failed with any of them, waiting until it has them all. */
export function loadAll(...states: Load[]): Load {
  return states.includes("error") ? "error" : states.includes("loading") ? "loading" : "ready";
}

/** A companion answer a block can do without once it has failed (it says so itself), but not while it is on its way. */
export const settled = (s: Load): Load => (s === "loading" ? "loading" : "ready");

/** Why the server refused the request `key` (a range: 400, a role: 403), kept while it is tried again: a live event
 *  refetches it, and react-query clears the error of a query without data for that time. */
export function useRefusal(key: string, status: 400 | 403, q: { data: unknown; error: unknown }): ApiError | null {
  const kept = useRef<{ key: string; error: ApiError }>(undefined);
  if (q.error instanceof ApiError && q.error.status === status) kept.current = { key, error: q.error };
  else if (q.data !== undefined || q.error || kept.current?.key !== key) kept.current = undefined;
  return kept.current?.error ?? null;
}

/** How many bars the skeleton of each shape has; their sizes are the stylesheet's (.cost-loading). */
const BONES = { line: 0, text: 2, hero: 3, table: 7, tiles: 4, figures: 3, chart: 2, page: 3 };
export type LoadingShape = keyof typeof BONES;

function Bones({ shape, busy }: { shape: LoadingShape; busy?: boolean }) {
  return (
    <span className="cost-loading-bones" aria-hidden="true" aria-busy={busy || undefined}>
      {Array.from({ length: BONES[shape] }, (_, i) => <i key={i} />)}
    </span>
  );
}

/** A page's placeholder shows after PAGE_DELAY (the stylesheet's, for the first), then for PAGE_HOLD at least. */
const PAGE_DELAY = 400, PAGE_HOLD = 300;
/** When the wait on screen began. The app's bundle and then the page's are one wait: the second placeholder goes on
 *  counting where the first was, or a blank would fall between the two. */
let waitingSince: number | undefined;

function PageLoading() {
  const { t } = useTranslation();
  const [{ since, delay }] = useState(() => {
    const now = performance.now(), from = waitingSince ?? now;
    return { since: from, delay: Math.round(PAGE_DELAY - (now - from)) };
  });
  useLayoutEffect(() => {
    waitingSince = since;
    return () => { waitingSince = undefined; };
  }, [since]);
  return (
    <span className="cost-loading page" style={{ animationDelay: `${delay}ms` }} data-cost-loading="page">
      <span className="cost-loading-text" role="status">{t("costs.home.loading")}</span>
      <Bones shape="page" busy />
    </span>
  );
}

/** A page's bundle, handed over at once unless its placeholder has only just appeared: that stays for a moment,
 *  rather than flashing. */
export async function held<T>(bundle: Promise<T>): Promise<T> {
  const module = await bundle;
  const left = waitingSince === undefined ? 0 : waitingSince + PAGE_DELAY + PAGE_HOLD - performance.now();
  if (left > 0 && left <= PAGE_HOLD) await new Promise((r) => setTimeout(r, left));
  return module;
}

/** The words, and the outline of what is to come at about its height. Plain words, so a link that holds them carries
 *  them in its name; the announcement is CostStatus's. */
export function CostLoading({ shape = "text" }: { shape?: LoadingShape }) {
  const { t } = useTranslation();
  if (shape === "page") return <PageLoading />;
  return (
    <span className={`cost-loading ${shape}`} aria-busy="true" data-cost-loading={shape}>
      <span className="cost-loading-text">{t("costs.home.loading")}</span>
      {BONES[shape] > 0 && <Bones shape={shape} />}
    </span>
  );
}

/** A view's one announcement that it waits. In the page before the words come into it, and outside what is busy:
 *  otherwise a screen reader may never read it. */
export function CostStatus({ waiting, className = "sr-only" }: { waiting: boolean; className?: string }) {
  const { t } = useTranslation();
  const [said, setSaid] = useState(false);
  useEffect(() => setSaid(waiting), [waiting]);
  return <span className={className} role="status" data-cost-status>{said ? t("costs.home.loading") : ""}</span>;
}

/** The room a filter row leaves free, and the wait said in it: on a phone the sticky bar covers half the screen, and
 *  the blocks' own words are often outside the other half. It never changes the row's size. */
export function CostBarWait({ waiting }: { waiting: boolean }) {
  return <div className="cost-bar-wait" style={{ flex: 1 }}><CostStatus waiting={waiting} className="cost-bar-wait-text" /></div>;
}

export function CostLoadError({ text }: { text?: string }) {
  const { t } = useTranslation();
  return <p className="muted text-sm mb-3" role="alert" data-cost-load-error>{text ?? t("costs.home.loadError")}</p>;
}

/** A bundle that failed stays failed until the page is loaded again (React.lazy keeps the rejection). */
export function CostBundleError() {
  const { t } = useTranslation();
  return <CostLoadError text={t("costs.home.bundleError")} />;
}

/** A block's stand-in until it has its answer: the wait, or the failure. */
export function CostWait({ state, shape, error }: { state: Load; shape?: LoadingShape; error?: string }) {
  if (state === "ready") return null;
  return state === "error" ? <CostLoadError text={error} /> : <CostLoading shape={shape} />;
}

export const isCostPath = (path: string) => path === "/costs" || path.startsWith("/costs/");

class LoadBoundary extends Component<{ children: ReactNode; on: boolean; failure: ReactNode }, { failed?: { error: unknown } }> {
  state: { failed?: { error: unknown } } = {};
  static getDerivedStateFromError(error: unknown) {
    return { failed: { error } };
  }
  render() {
    if (!this.state.failed) return this.props.children;
    if (!this.props.on) throw this.state.failed.error;
    return this.props.failure;
  }
}

/** A lazily loaded piece of the cost pages: what stands in while its bundle is on its way, and the error where it
 *  never arrives (a build that replaced it, a dropped connection) instead of an empty screen. Where it wraps more
 *  than the cost pages (the app's shell), `on` is false on the others: no placeholder, and no error of these pages'. */
export function CostBundle({ children, on = true, fallback = <CostLoading shape="page" />, failure = <CostBundleError /> }: {
  children: ReactNode; on?: boolean; fallback?: ReactNode; failure?: ReactNode;
}) {
  return <LoadBoundary on={on} failure={failure}><Suspense fallback={on ? fallback : null}>{children}</Suspense></LoadBoundary>;
}
