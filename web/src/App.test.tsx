import type { ReactElement } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, render, waitFor, within, act, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import App from "./App";
import { api } from "./api";
import { merkeSprache } from "./i18n";
import { useGerman, testPrincipal } from "./test/render";

/* What happens when the session ends.

   Two paths lead there and both ended in nothing before: whoever reloaded the
   page landed on the 404 of the public website under their app address (which
   does not know /agents/…); whoever clicked on stayed in a shell that filled
   with error messages. Both should end at the
   login, carrying the way back. */

// App renders its own routes; the scaffold from test/render.tsx hangs them
// under a splat route and would shift the paths. So directly here.
function renderApp(ui: ReactElement, route: string) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

/* A server that knows the session and then does not. Everything except
   /auth/me is beside the point, the shell asks for a number of things while it
   builds that carry nothing here. */
function serverMitSitzung(angemeldet: () => boolean, teamSurface = false) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      const abgelehnt = new Response(JSON.stringify({ error: "session expired" }), { status: 401 });
      if (!angemeldet()) return abgelehnt;
      if (url.includes("/auth/me")) {
        return new Response(JSON.stringify({ ...testPrincipal(), TeamSurface: teamSurface }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      // Two queries of the shell expect an object instead of a list: the setup
      // checklist and the build line in the footer.
      const leer = url.includes("/onboarding")
        ? { steps: [], done: true }
        : url.includes("/version")
          ? { version: "test", commit: "0000000", go: "", built_at: "", dirty: false }
          : [];
      return new Response(JSON.stringify(leer), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

/* The shell opens an event stream; jsdom does not know EventSource. The
   stand-in keeps its instances and lets a test play the browser: set
   readyState, fire onerror or onopen. */
class FakeEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;
  static instances: FakeEventSource[] = [];
  readyState = FakeEventSource.OPEN;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    FakeEventSource.instances.push(this);
  }
  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
  addEventListener() {}
}

beforeEach(() => {
  useGerman();
  merkeSprache("de");
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  /* The public website brings its own background (canvas, scroll reveals).
     jsdom has neither; without stand-ins the login page dies on its
     decoration before the test sees it. */
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
});

describe("App ohne gültige Sitzung", () => {
  it("leitet von einer Adresse der Oberfläche auf die Anmeldung um", async () => {
    serverMitSitzung(() => false);
    renderApp(<App />, "/agents/1234");

    // Not the 404 of the public website, but the login form …
    expect(await screen.findByLabelText("Passwort")).toBeInTheDocument();
    // … and a sentence why one stands here again (the ?weiter= parameter from
    // the redirect carries it).
    expect(screen.getByText(/Sitzung ist abgelaufen/)).toBeInTheDocument();
  });

  it("lässt die Adressen aus einer Mail durch, statt sie zur Anmeldung zu schicken", async () => {
    /* The case /reset and /verify were built for was the one case where they
       did not work: someone who forgot their password is not signed in. The
       redirect reached before them and sent them to /anmelden?weiter=%2Freset,
       which is where they cannot get further, with the notice that their
       session had expired. The same path hit the confirmation link from the
       signup mail (#210). */
    serverMitSitzung(() => false);
    renderApp(<App />, "/reset");

    expect(await screen.findByText("Passwort zurücksetzen")).toBeInTheDocument();
    expect(screen.getByLabelText("E-Mail-Adresse")).toBeInTheDocument();
    expect(screen.queryByText(/Sitzung ist abgelaufen/)).not.toBeInTheDocument();
  });

  it("zeigt auf der Wurzel die Anmeldung, ohne von einer Sitzung zu reden", async () => {
    serverMitSitzung(() => false);
    renderApp(<App />, "/");

    /* Since the website moved out (#130), "/" here is the login and nothing
       else. Whoever was never signed in should therefore not read that their
       session expired. */
    expect(await screen.findByLabelText("Passwort")).toBeInTheDocument();
    expect(screen.queryByText(/Sitzung ist abgelaufen/)).not.toBeInTheDocument();
  });
});

describe("App bei ablaufender Sitzung", () => {
  it("schaltet auf die Anmeldung um, sobald der Server mit 401 antwortet", async () => {
    let angemeldet = true;
    serverMitSitzung(() => angemeldet);
    renderApp(<App />, "/inbox");

    // Signed in: the shell stands (the navigation is its most visible part).
    expect(await screen.findByText("Agenten")).toBeInTheDocument();

    // The session ends on the server side; the next request of the UI brings
    // it to light.
    angemeldet = false;
    await api("/agents").catch(() => {});

    await waitFor(() => expect(screen.getByLabelText("Passwort")).toBeInTheDocument());
    expect(screen.queryByText("Agenten")).not.toBeInTheDocument();
  });

  it("landet auch von der Übersicht aus auf der Anmeldung", async () => {
    /* Whoever loses the session on the dashboard should see the login and read
       why they stand in front of it again, also from the agent list, where a
       first visitor does not get the sentence just now. The list moved from
       "/" to "/agents" when the workspace took the root. */
    let angemeldet = true;
    serverMitSitzung(() => angemeldet);
    renderApp(<App />, "/agents");

    // On the dashboard `Agenten` stands twice: in the navigation and as a
    // heading.
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    angemeldet = false;
    await api("/agents").catch(() => {});

    await waitFor(() => expect(screen.getByLabelText("Passwort")).toBeInTheDocument());
    expect(screen.getByText(/Sitzung ist abgelaufen/)).toBeInTheDocument();
  });
});

/* The tab comes back to the foreground: what react-query listens to for
   refetchOnWindowFocus. */
const fokus = () => act(() => void document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));

/* What /auth/me answers is the one thing that changes in these tests; the rest
   of the server stays as serverMitSitzung has it. */
function serverMitMe(antwort: () => "ok" | "401" | "502" | "netz", verzoegerung = 0) {
  serverMitSitzung(() => true);
  const server = globalThis.fetch;
  const aufrufe = { ok: 0, gesamt: 0 };
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    if (!String(input).includes("/auth/me")) return server(input, init);
    aufrufe.gesamt++;
    if (verzoegerung) await new Promise((r) => setTimeout(r, verzoegerung));
    const a = antwort();
    if (a === "netz") throw new TypeError("Failed to fetch");
    if (a === "ok") aufrufe.ok++;
    if (a !== "ok") return new Response(JSON.stringify({ error: a }), { status: a === "401" ? 401 : 502 });
    return server(input, init);
  });
  return aufrufe;
}

/* #115: the login form was reset on every tab focus, and a signed-in tab
   showed the login form while covey restarted. Both because anything that
   went wrong with /auth/me looked like "not signed in". */
describe("App und /auth/me im Hintergrund", () => {
  it("behält, was in die Anmeldung getippt wurde, wenn der Tab den Fokus zurückbekommt", async () => {
    serverMitMe(() => "401", 20);
    renderApp(<App />, "/");

    const passwort = await screen.findByLabelText("Passwort");
    fireEvent.change(passwort, { target: { value: "geheim" } });

    await fokus();
    await new Promise((r) => setTimeout(r, 150));

    // The same field, not a new one that happens to look alike.
    expect(screen.getByLabelText("Passwort")).toBe(passwort);
    expect(passwort.isConnected).toBe(true);
    expect(passwort).toHaveValue("geheim");
  });

  for (const fehler of ["502", "netz"] as const) {
    it(`lässt die Oberfläche stehen, wenn /auth/me im Hintergrund fehlschlägt (${fehler}), und erholt sich`, async () => {
      let antwort: "ok" | "401" | "502" | "netz" = "ok";
      const aufrufe = serverMitMe(() => antwort);
      renderApp(<App />, "/agents");
      expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

      // covey restarts: the stream breaks, the focus check runs into the gap.
      antwort = fehler;
      const vorher = aufrufe.gesamt;
      await fokus();
      await waitFor(() => expect(aufrufe.gesamt).toBeGreaterThan(vorher));
      await new Promise((r) => setTimeout(r, 100));
      expect(screen.queryByLabelText("Passwort")).not.toBeInTheDocument();
      expect(screen.getAllByText("Agenten").length).toBeGreaterThan(0);

      // It is back: the query finds out on its own retry, nobody reloads.
      antwort = "ok";
      const ok = aufrufe.ok;
      await waitFor(() => expect(aufrufe.ok).toBeGreaterThan(ok), { timeout: 5000 });
      expect(screen.queryByLabelText("Passwort")).not.toBeInTheDocument();
      expect(screen.getAllByText("Agenten").length).toBeGreaterThan(0);
    });
  }

  it("zeigt die Anmeldung, wenn /auth/me im Hintergrund mit 401 antwortet", async () => {
    let antwort: "ok" | "401" = "ok";
    const aufrufe = serverMitMe(() => antwort);
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    antwort = "401";
    const vorher = aufrufe.gesamt;
    await fokus();

    // Expiry still ends the UI: only a 401 does.
    expect(await screen.findByLabelText("Passwort")).toBeInTheDocument();
    expect(aufrufe.gesamt).toBeGreaterThan(vorher);
    expect(screen.queryByText("Agenten")).not.toBeInTheDocument();
  });
});

/* The event stream and /auth/me work together (#115): an error on the stream
   asks /auth/me whether the session is still there. */
describe("App und der Ereignisstrom", () => {
  const streams = () => FakeEventSource.instances;

  it("öffnet einen neuen Strom, wenn der Browser ihn nach einem 502 endgültig geschlossen hat", async () => {
    /* A proxy that answers 502 while covey restarts makes the browser fail
       the connection for good: readyState CLOSED, no reconnect of its own.
       The session is intact, so the shell stands and nothing else would
       open a stream again. */
    let antwort: "ok" | "502" = "ok";
    const aufrufe = serverMitMe(() => antwort);
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);
    expect(streams()).toHaveLength(1);

    antwort = "502";
    const vorher = aufrufe.gesamt;
    streams()[0].readyState = FakeEventSource.CLOSED;
    act(() => streams()[0].onerror?.());
    await waitFor(() => expect(aufrufe.gesamt).toBeGreaterThan(vorher));
    expect(streams()).toHaveLength(1);

    // The server is back: /auth/me answers again, and a stream follows.
    antwort = "ok";
    await waitFor(() => expect(streams()).toHaveLength(2), { timeout: 5000 });
    expect(screen.queryByLabelText("Passwort")).not.toBeInTheDocument();
  }, 10_000);

  it("lässt einen Strom stehen, den der Browser selbst wieder aufbaut", async () => {
    const aufrufe = serverMitMe(() => "ok");
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    const vorher = aufrufe.gesamt;
    streams()[0].readyState = FakeEventSource.CONNECTING;
    act(() => streams()[0].onerror?.());
    await waitFor(() => expect(aufrufe.gesamt).toBeGreaterThan(vorher));
    await new Promise((r) => setTimeout(r, 100));
    expect(streams()).toHaveLength(1);
  });

  it("holt beim ersten Öffnen nach einer Lücke nach, was die Ereignisse gemeldet hätten", async () => {
    serverMitMe(() => "ok");
    const server = globalThis.fetch;
    let agentenListen = 0;
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
      if (/\/agents(\?|$)/.test(String(input))) agentenListen++;
      return server(input, init);
    });
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    // The first open is no gap: nothing to catch up on.
    act(() => streams()[0].onopen?.());
    await new Promise((r) => setTimeout(r, 100));
    const vorher = agentenListen;

    streams()[0].readyState = FakeEventSource.CLOSED;
    act(() => streams()[0].onerror?.());
    await waitFor(() => expect(streams()).toHaveLength(2));
    act(() => streams()[1].onopen?.());
    await waitFor(() => expect(agentenListen).toBeGreaterThan(vorher));
    // Once, not on every open.
    const nachgeholt = agentenListen;
    act(() => streams()[1].onopen?.());
    await new Promise((r) => setTimeout(r, 100));
    expect(agentenListen).toBe(nachgeholt);
  });

  it("wartet länger, wenn der neue Strom sich nicht öffnet, statt den Server zu bedrängen", async () => {
    serverMitMe(() => "ok");
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    streams()[0].readyState = FakeEventSource.CLOSED;
    act(() => streams()[0].onerror?.());
    await waitFor(() => expect(streams()).toHaveLength(2));

    // The second one fails before it ever opened: the third waits.
    streams()[1].readyState = FakeEventSource.CLOSED;
    act(() => streams()[1].onerror?.());
    await new Promise((r) => setTimeout(r, 400));
    expect(streams()).toHaveLength(2);
    await waitFor(() => expect(streams()).toHaveLength(3), { timeout: 3000 });
  });

  it("fragt /auth/me bei mehreren Fehlern während der Pause nur einmal", async () => {
    /* The browser fires error again and again while it reconnects. Without
       cancelRefetch: false each one would cancel the running check, together
       with the pause of its retry, and start over. */
    let antwort: "ok" | "502" = "ok";
    const aufrufe = serverMitMe(() => antwort);
    renderApp(<App />, "/agents");
    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);

    antwort = "502";
    const vorher = aufrufe.gesamt;
    for (let i = 0; i < 3; i++) act(() => streams()[0].onerror?.());
    await new Promise((r) => setTimeout(r, 200));
    expect(aufrufe.gesamt - vorher).toBe(1);
  });
});

/* The first load, before there is any data (#115). */
describe("App beim ersten Laden ohne Antwort", () => {
  it("rendert nichts und kommt hoch, sobald der Browser wieder online ist", async () => {
    /* Offline, the first fetch is paused: neither loading nor an error, and
       no data. The shell must not read me.data then. */
    serverMitSitzung(() => true);
    onlineManager.setOnline(false);
    try {
      const { container } = renderApp(<App />, "/agents");
      await new Promise((r) => setTimeout(r, 100));
      expect(container).toBeEmptyDOMElement();
      expect(screen.queryByLabelText("Passwort")).not.toBeInTheDocument();

      act(() => onlineManager.setOnline(true));
      expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);
    } finally {
      onlineManager.setOnline(true);
    }
  });

  it("fragt in einem Ausfall höchstens alle fünf Sekunden nach", async () => {
    /* No data means no stream and no focus check to notice that the server is
       back, so the page would stay blank up to the 30 s cap. Attempts at
       0, 1, 3, 7, 12 s with the lower cap; 0, 1, 3, 7, 15 s without. */
    vi.useFakeTimers();
    try {
      const aufrufe = serverMitMe(() => "502");
      renderApp(<App />, "/agents");
      await act(() => vi.advanceTimersByTimeAsync(12_100));
      expect(aufrufe.gesamt).toBe(5);
    } finally {
      vi.useRealTimers();
    }
  });
});

/* The team surface is an opt-in per organisation (#328). Which shell the root
   gets is read from /auth/me, and off must mean the console at the root and no
   switch to a surface that is not there. */
describe("App und die Team-Oberfläche", () => {
  it("gibt die Wurzel der Verwaltung, solange die Organisation das Team nicht eingeschaltet hat", async () => {
    serverMitSitzung(() => true, false);
    renderApp(<App />, "/");

    expect((await screen.findAllByText("Agenten")).length).toBeGreaterThan(0);
    // The rail has no team to go to (#389).
    expect(screen.getByRole("navigation", { name: "Bereiche" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Team" })).not.toBeInTheDocument();
  });

  it("gibt die Wurzel dem Büro, im selben Rahmen wie die Agentenliste", async () => {
    // The office measures its floor; jsdom does not.
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
    serverMitSitzung(() => true, true);
    const { container } = renderApp(<App />, "/");

    expect(await screen.findByRole("heading", { name: "Guten Tag, Test" })).toBeInTheDocument();
    /* The office is a view of Agents, a main view: the rail marks Agents,
       and no Admin column stands beside it, as none stands beside the
       list. */
    expect(screen.getByRole("link", { name: "Agenten" })).toHaveAttribute("aria-current", "page");
    expect(within(screen.getByRole("navigation", { name: "Bereiche" })).queryByRole("link", { name: /^Büro/ })).not.toBeInTheDocument();
    // Its place is the switch in the area's bar, and it is the view one is in.
    expect(
      within(screen.getByRole("navigation", { name: "Ansichten der Agenten" })).getByRole("link", { name: "Büro" }),
    ).toHaveAttribute("aria-current", "page");
    expect(container.querySelector("#konsole-nav")).toBeNull();
    // Admin lands on the org chart, the first item of its column.
    expect(screen.getByRole("link", { name: "Verwaltung" }).getAttribute("href")).toBe("/org");
  });

  it("lässt die Unterhaltung im Team und zeigt die Agentenseite ohne Spalte der Verwaltung", async () => {
    serverMitSitzung(() => true, true);
    const { container, unmount } = renderApp(<App />, "/team/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa");
    await waitFor(() => expect(screen.getByRole("link", { name: "Team" })).toHaveAttribute("aria-current", "page"));
    expect(container.querySelector("#konsole-nav")).toBeNull();
    unmount();

    const agent = renderApp(<App />, "/agents/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa");
    await waitFor(() => expect(screen.getByRole("link", { name: "Agenten" })).toHaveAttribute("aria-current", "page"));
    expect(agent.container.querySelector("#konsole-nav")).toBeNull();
  });

  it("führt ohne Team-Oberfläche von einer Unterhaltung zum Agenten, ohne Spalte der Verwaltung", async () => {
    serverMitSitzung(() => true, false);
    const { container } = renderApp(<App />, "/team/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa");
    await waitFor(() => expect(screen.getByRole("link", { name: "Agenten" })).toHaveAttribute("aria-current", "page"));
    expect(screen.queryByRole("link", { name: "Team" })).not.toBeInTheDocument();
    expect(container.querySelector("#konsole-nav")).toBeNull();
  });
});

/* The pairing link lands here when no app took it (#333): the same page
   signed in or not — the code is the badge, the login has nothing to add. */
describe("App und der Kopplungslink", () => {
  for (const angemeldet of [false, true]) {
    it(`reicht /pair an die App weiter (${angemeldet ? "angemeldet" : "abgemeldet"})`, async () => {
      serverMitSitzung(() => angemeldet);
      renderApp(<App />, "/pair?code=coveypair_abc");

      const link = await screen.findByRole("link", { name: "In der App öffnen" });
      expect(link.getAttribute("href")).toBe(
        `covey://pair?instance=${encodeURIComponent(window.location.origin)}&code=coveypair_abc`,
      );
      expect(screen.queryByLabelText("Passwort")).not.toBeInTheDocument();
    });
  }
});

/* At phone width the console's navigation is a drawer behind a button in a
   bar above the page (#463): the button says whether it is open, Escape and
   the scrim close it, a link closes it, and focus comes back to the button. */
describe("App bei Telefonbreite", () => {
  beforeEach(() => {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q.includes("max-width"),
      addEventListener() {},
      removeEventListener() {},
    }));
  });

  it("legt die Navigation der Verwaltung hinter einen Knopf", async () => {
    serverMitSitzung(() => true);
    // The inbox reads an object, not a list; everything else may stay empty.
    const server = globalThis.fetch;
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
      String(input).includes("/inbox")
        ? Promise.resolve(new Response(JSON.stringify({ items: [], pending: 0 }), { status: 200, headers: { "Content-Type": "application/json" } }))
        : server(input, init),
    );
    const { container } = renderApp(<App />, "/org");

    const knopf = await screen.findByRole("button", { name: "Menü" });
    const leiste = container.querySelector("#konsole-nav")!;
    expect(knopf).toHaveAttribute("aria-expanded", "false");
    expect(knopf).toHaveAttribute("aria-controls", "konsole-nav");
    expect(leiste).toHaveAttribute("inert");
    // The bar names the page the hidden navigation would otherwise mark.
    expect(container.querySelector(".konsole-bar-titel")).toHaveTextContent("Organigramm");

    // Open: a dialog, focus inside it, on the page one is on.
    knopf.click();
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "true"));
    expect(leiste).not.toHaveAttribute("inert");
    expect(leiste).toHaveAttribute("role", "dialog");
    expect(leiste).toHaveAttribute("aria-modal", "true");
    expect(leiste.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toHaveTextContent("Organigramm");

    // Escape closes it and gives focus back to the button.
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "false"));
    expect(document.activeElement).toBe(knopf);

    // The scrim closes it.
    knopf.click();
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "true"));
    (container.querySelector(".konsole-scrim") as HTMLElement).click();
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "false"));

    // A link navigates and closes it.
    knopf.click();
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "true"));
    const kosten = [...leiste.querySelectorAll("a")].find((a) => a.textContent === "Kosten")!;
    kosten.click();
    await waitFor(() => expect(knopf).toHaveAttribute("aria-expanded", "false"));
    await waitFor(() => expect(container.querySelector(".konsole-bar-titel")).toHaveTextContent("Kosten"));
  });
});
