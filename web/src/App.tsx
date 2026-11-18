import { Suspense, lazy, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate, useLocation } from "react-router";
import { api, isUnauthorized, setUnauthorizedHandler, type Principal } from "./api";
import { initialLang } from "./i18n";
import { APP_LINK_PATHS, LANGS, MAIL_LINK_PATHS, PUBLIC_ROUTES, pathOf } from "./public/routes";
import SignedOut from "./public/SignedOut";

/* The signed-in UI comes from its own bundle — it is the larger part of the
   application and none of the business of someone who reads the public
   website (#122). No placeholder: nothing stood here anyway while
   /auth/me ran. */
const AppShell = lazy(() => import("./AppShell"));
const Team = lazy(() => import("./team/Team"));
const NoOrganization = lazy(() => import("./pages/NoOrganization"));
const PairLink = lazy(() => import("./pages/PairLink"));

/* Two shells, and the address decides which one applies.
 *
 * The TEAM is the surface of whoever works WITH the workforce: threads,
 * open questions, an input field. The console is the surface of whoever
 * BUILDS it: configuration, access, guard rails, costs. The two share
 * neither navigation nor purpose — a tab among thirteen others would have
 * claimed it was the same work.
 *
 * The root used to belong to the team, because everyone lands there. Since
 * agents are a main view of their own, the office at the root is one of
 * its views, beside the list under /agents and every agent's page, and the
 * console renders all three in one frame. The team keeps the
 * conversations. */
const imTeam = (pfad: string) => pfad === "/team" || pfad.startsWith("/team/");

// useLiveEvents keeps the UI current over SSE: every server event invalidates
// the queries it touches, and TanStack Query refetches just those.
function useLiveEvents(enabled: boolean) {
  const qc = useQueryClient();
  /* A stream the browser gave up on is replaced by a new one (see onerror).
     These three outlive one stream: the generation that makes the effect
     run again, how many replacements in a row ended without the stream
     opening, and whether events may have been missed in between. */
  const [generation, setGeneration] = useState(0);
  const failures = useRef(0);
  const missed = useRef(false);
  useEffect(() => {
    if (!enabled) return;
    let ended = false;
    let replacing = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const es = new EventSource("/api/v1/events");
    const invalidate = () => {
      qc.invalidateQueries({ queryKey: ["agents"] });
      qc.invalidateQueries({ queryKey: ["agent"] });
      qc.invalidateQueries({ queryKey: ["backlog"] });
      qc.invalidateQueries({ queryKey: ["recording"] });
      qc.invalidateQueries({ queryKey: ["inbox"] });
      qc.invalidateQueries({ queryKey: ["cost"] });
      qc.invalidateQueries({ queryKey: ["memories"] });
      /* Der Verlauf und der Grundriss hängen an denselben Ereignissen. Sie
         standen bis zuletzt auf einem Zehn-Sekunden-Takt, weil es für sie
         keinen Push gab — den gibt es jetzt (chat.go), und ein Takt, der
         daneben weiterläuft, wäre nur noch das Netz darunter. */
      qc.invalidateQueries({ queryKey: ["thread"] });
      qc.invalidateQueries({ queryKey: ["threads"] });
      qc.invalidateQueries({ queryKey: ["conversation"] });
      qc.invalidateQueries({ queryKey: ["conversations"] });
      qc.invalidateQueries({ queryKey: ["org-running"] });
    };
    for (const t of ["agent_status", "task", "recording", "approval", "guardrail", "chat"]) {
      es.addEventListener(t, invalidate);
    }
    // The roster changed (#619): an agent created, hired, renamed, moved or
    // deleted, a department added or changed — in another tab, or by an
    // agent drafting a colleague.
    es.addEventListener("roster", () => {
      invalidate();
      qc.invalidateQueries({ queryKey: ["departments"] });
      qc.invalidateQueries({ queryKey: ["org-chart"] });
      qc.invalidateQueries({ queryKey: ["orgchart"] });
    });
    /* A broken event stream can have two causes: the server was restarted
       (then the browser reconnects on its own) — or the session expired and
       the endpoint answers with 401. An EventSource does not say which of
       the two. So the UI asks: /auth/me answers it, and an expired session
       lands in its 401 without anyone having
       to click first. Only that 401 ends the session (#115): while the server
       restarts, /auth/me fails with a network error or a 502 and the query
       keeps retrying behind the UI that stands.

       The browser reconnects only after a network error. A reverse proxy
       that answers 502 while covey restarts makes it close the stream for
       good (readyState CLOSED), and the shell would stand without live
       updates until a reload. So once /auth/me answers again, a closed
       stream gets a new one — with a growing pause when that keeps failing —
       and the first open after a gap refetches what the events would have
       told. */
    es.onopen = () => {
      failures.current = 0;
      if (!missed.current) return;
      missed.current = false;
      invalidate();
    };
    es.onerror = () => {
      missed.current = true;
      void qc.refetchQueries({ queryKey: ["me"] }, { cancelRefetch: false }).then(() => {
        if (ended || replacing || es.readyState !== EventSource.CLOSED) return;
        // A 401 leaves the query in error: the session is over, nothing to reopen.
        if (qc.getQueryState(["me"])?.status !== "success") return;
        replacing = true;
        const pause = failures.current === 0 ? 0 : Math.min(1000 * 2 ** (failures.current - 1), 30_000);
        failures.current++;
        timer = setTimeout(() => setGeneration((g) => g + 1), pause);
      });
    };
    return () => {
      ended = true;
      clearTimeout(timer);
      es.close();
    };
  }, [enabled, qc, generation]);
}

/* The zero UUID is the server's answer to "no organisation yet":
   Principal.OrgID is a value, not a pointer, so it has no NULL. */
const LEERE_UUID = "00000000-0000-0000-0000-000000000000";

/* Not signed in (anymore): every address except the open ones leads to the
   login, carrying the destination along (?weiter=) so it returns to where
   someone was interrupted.

   Until #130 this was a case distinction, because "/" was two things at once:
   the dashboard of the UI and the landing page of the website. Whoever lost
   their session landed on the marketing page instead of the form, and the
   rule had to tell apart someone who had just fallen out from someone newly
   arriving. Since the website sits on its own host, "/" here is one thing
   only. */
function Abgemeldet({
  onLogin,
  ausDerOberflaeche = false,
}: {
  onLogin: () => void;
  ausDerOberflaeche?: boolean;
}) {
  const { pathname, search } = useLocation();
  /* Open are login and signup in every language, and the two addresses a mail
     points at (/verify, /reset).

     The second half was missing, so the case they were built for was the one
     case where they did not work: someone who forgot their password is not
     signed in. They landed on /anmelden?weiter=%2Freset, which is
     exactly where they cannot get further, with the notice that their
     session had expired. The confirmation link out of the
     signup mail hit the same.

     SignedOut knows both routes; they were
     never reached, because this redirected first (#210). */
  const offen =
    PUBLIC_ROUTES.some((r) => LANGS.some((l) => r.path[l] === pathname)) ||
    MAIL_LINK_PATHS.includes(pathname);
  if (!offen) {
    /* The language follows the stored choice, else the browser, not the path,
       which carries none for app addresses. */
    const lang = initialLang("/");
    const anmeldung = pathOf("anmelden", lang);
    /* ?weiter= carries two things: where to go back to after the login, and
       the sentence `Ihre Sitzung ist abgelaufen` (LoginCard reads the
       parameter). From the bare root the first does not exist — "/" is where
       the UI starts anyway. Whoever just had their session expire underneath
       still gets the sentence: that is what ausDerOberflaeche is for.
       Otherwise it would stand in front of every first visitor who calls /
       and has never been signed in. */
    const mitZiel = ausDerOberflaeche || pathname !== "/";
    const ziel = mitZiel
      ? `${anmeldung}?weiter=${encodeURIComponent(pathname + search)}`
      : anmeldung;
    return <Navigate to={ziel} replace />;
  }
  return <SignedOut onLogin={onLogin} />;
}

/* A safe ?weiter=: only a path of this installation. "//host" is a foreign
   address to the browser, and an open redirector at the login is one of the
   oldest phishing aids. */
function weiterZiel(search: string): string | null {
  const ziel = new URLSearchParams(search).get("weiter");
  if (!ziel || !ziel.startsWith("/") || ziel.startsWith("//")) return null;
  return ziel;
}

export default function App() {
  const qc = useQueryClient();
  const location = useLocation();
  /* Expired means: the server rejected a request of the running session with
     401. Without this state the shell stayed standing and filled up with
     errors — the ["me"] query did not rerun and knew
     nothing of it. */
  const [abgelaufen, setAbgelaufen] = useState(false);
  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api<Principal>("/auth/me"),
    /* Only a 401 says "not signed in". Anything else — a network error, a 502
       while covey restarts — leaves the session as it was: the query retries
       with a growing pause and the UI that stands stays (#115). */
    retry: (_n, err) => !isUnauthorized(err),
    /* Without data there is no stream and no focus refetch to notice that the
       server is back, so the first load polls at a shorter cap (a blank page
       is all there is to see meanwhile). */
    retryDelay: (n) => Math.min(1000 * 2 ** n, qc.getQueryData(["me"]) === undefined ? 5_000 : 30_000),
    /* The common case is the tab that stays open overnight: on return the UI
       should check the session instead of walking into a 401 on
       the first click. Not while signed out: a query without data goes back
       to pending when it fetches, the empty state then unmounts the login
       form, and whatever was typed into it is gone (#115). */
    refetchOnWindowFocus: (q) => q.state.data !== undefined,
  });

  useEffect(() => {
    setUnauthorizedHandler(() => setAbgelaufen(true));
    return () => setUnauthorizedHandler(null);
  }, []);

  const anmelden = () => {
    /* Drop the data of the ended session — else the UI would show the lists
       of the previous person for a moment
       after a login as someone else. */
    qc.clear();
    setAbgelaufen(false);
    void me.refetch();
  };

  useLiveEvents(me.isSuccess && !abgelaufen);

  /* The pairing link (#333) is the same page signed in or not: the code in it
     is the badge, and neither the login nor a shell has anything to add. */
  if (APP_LINK_PATHS.includes(location.pathname)) {
    return (
      <Suspense fallback={null}>
        <PairLink />
      </Suspense>
    );
  }
  if (abgelaufen) return <Abgemeldet onLogin={anmelden} ausDerOberflaeche />;
  /* Nothing renders while /auth/me runs: a form that the UI replaces a moment
     later is more restless than a short emptiness. Until #130 the pre-rendered
     website stood here, and that was already in the HTML, so there was nothing
     to show from here. */
  if (me.isLoading) return null;
  if (me.isError && isUnauthorized(me.error)) return <Abgemeldet onLogin={anmelden} />;
  /* No data and no 401: the server is not answering yet and the query is
     retrying — or it is paused because the browser is offline, which is
     neither loading nor an error (and used to run into me.data! below). */
  if (!me.data) return null;
  /* Signed in but without a seat: since the login hangs on the account
     (FR-002), an account can exist before an organisation knows it. The shell
     would run into nothing but 409s there, so it does not get that far. */
  if (me.data!.OrgID === LEERE_UUID) {
    return (
      <Suspense fallback={null}>
        <NoOrganization me={me.data!} onLogout={() => me.refetch()} />
      </Suspense>
    );
  }
  /* Signed in again: back to the place where the session broke off. */
  const weiter = weiterZiel(location.search);
  if (weiter) return <Navigate to={weiter} replace />;
  return (
    <Suspense fallback={null}>
      {/* Only for an organisation that has turned it on (#328). Otherwise the
          root and /team/* belong to the console, which sends them on. */}
      {me.data!.TeamSurface && imTeam(location.pathname) ? (
        <Team me={me.data!} onLogout={() => me.refetch()} />
      ) : (
        <AppShell me={me.data!} onLogout={() => me.refetch()} />
      )}
    </Suspense>
  );
}

