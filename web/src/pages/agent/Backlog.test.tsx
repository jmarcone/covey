import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { useState } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Backlog } from "./Backlog";
import HelpDrawer from "../../components/HelpDrawer";
import { mockFetch, renderWithProviders, useGerman } from "../../test/render";

// An agent without write access to the tracker of the platform is the normal
// case — creating issues is a write access under an identity, and that does
// not belong in a sandbox. What the agent writes down must therefore be
// reported by a human, and the note is the place where they read it. The link
// takes the typing off them, not the decision.

const ORG = {
  id: "org-1",
  name: "Northgate",
  description: "",
  platform_repo_system: "",
  platform_repo_project: "",
  fleet_killed: false,
  human_count: 1,
  agent_count: 1,
  created_at: "2026-01-01T00:00:00Z",
};

const BUILD = {
  version: "v0.4.0",
  commit: "abc1234",
  built_at: "2026-08-26T19:54:51Z",
  go: "go1.26",
  source: "https://github.com/benjaminLedel/covey",
  source_system: "github",
  source_project: "benjaminLedel/covey",
};

const TASK = {
  id: "t1",
  agent_id: "a1",
  title: "gitlab/checkout räumt fremde Arbeitsbäume",
  body: "",
  state: "done",
  priority: 3,
  origin: "manual",
  created_at: "2026-08-26T18:00:00Z",
  updated_at: "2026-08-26T18:30:00Z",
};

const NOTE = {
  id: "n1",
  task_id: "t1",
  author: "covey-doctor",
  content: "Vier reproduzierbare Eigenheiten kosten regelmäßig Turns.",
  created_at: "2026-08-26T18:30:00Z",
};

const routen = (org = ORG) => ({
  "/api/v1/agents/a1/backlog": [TASK],
  "/api/v1/agents/a1/stages": [],
  "/api/v1/tasks/t1/notes": [NOTE],
  "/api/v1/org": org,
  "/api/v1/version": BUILD,
});

async function oeffneAufgabe() {
  await userEvent.click(await screen.findByText(TASK.title));
}

describe("Befund aus einer Aufgaben-Notiz melden", () => {
  beforeEach(() => useGerman());

  it("bietet einen vorbefüllten Link auf das Projekt, aus dem die Instanz stammt", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await oeffneAufgabe();

    const link = (await screen.findByText("↗ nach oben melden")) as HTMLAnchorElement;
    const url = new URL(link.href);
    expect(url.origin + url.pathname).toBe("https://github.com/benjaminLedel/covey/issues/new");
    expect(url.searchParams.get("title")).toBe(TASK.title);
    // The body carries the finding AND its origin: who wrote it down and which
    // build ran — without that the report costs the maintainer a round of
    // questions.
    const body = url.searchParams.get("body")!;
    expect(body).toContain(NOTE.content);
    expect(body).toContain("covey-doctor");
    expect(body).toContain("abc1234");
    // Nothing is sent by itself.
    expect(link.target).toBe("_blank");
    expect(link.rel).toContain("noopener");
  });

  // The address is NOT negotiable: what the organisation enters for its own
  // processes is its repository — a bug of the platform belongs where the
  // platform is maintained.
  it("zeigt auch dann auf das Hauptprojekt, wenn die Organisation ein eigenes Repo führt", async () => {
    mockFetch(routen({ ...ORG, platform_repo_system: "gitlab", platform_repo_project: "intern/covey" }));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await oeffneAufgabe();

    const link = (await screen.findByText("↗ nach oben melden")) as HTMLAnchorElement;
    expect(link.href).toContain("github.com/benjaminLedel/covey/issues/new");
    expect(link.href).not.toContain("intern/covey");
  });

  // A binary without a known origin (own build without the constant) has no
  // target — then no button belongs there either.
  it("zeigt keinen Link, wo die Herkunft unbekannt ist", async () => {
    mockFetch({ ...routen(), "/api/v1/version": { ...BUILD, source_system: "", source_project: "" } });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await oeffneAufgabe();

    expect(await screen.findByText(NOTE.content)).toBeTruthy();
    expect(screen.queryByText("↗ nach oben melden")).toBeNull();
  });
});

// A phone shows the stages stacked: one heading per stage, folded by its
// button. What you came for is open — the first stage with a task and the
// stage holding the running one — and the rest stays shut.
describe("Backlog auf dem Telefon", () => {
  beforeEach(() => {
    useGerman();
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q === "(max-width: 768px)",
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
  });
  afterEach(() => vi.unstubAllGlobals());

  const stufen = [
    { id: "s1", name: "Neu", color: "", position: 0 },
    { id: "s2", name: "Wartet", color: "", position: 1 },
    { id: "s3", name: "Läuft", color: "", position: 2 },
    { id: "s4", name: "Leer", color: "", position: 3 },
  ];
  const aufgabe = (id: string, stage_id: string, state: string) => ({
    ...TASK,
    id,
    title: `Aufgabe ${id}`,
    stage_id,
    state,
  });

  it("öffnet die erste gefüllte Stufe und die mit der laufenden Aufgabe", async () => {
    mockFetch({
      ...routen(),
      "/api/v1/agents/a1/stages": stufen,
      "/api/v1/agents/a1/backlog": [
        aufgabe("x1", "s2", "open"),
        aufgabe("x2", "s3", "in_progress"),
      ],
    });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    const wartet = await screen.findByRole("button", { name: /Wartet/ });
    const neu = screen.getByRole("button", { name: /^Neu(?!e)/ });
    const laeuft = screen.getByRole("button", { name: /Läuft/ });
    const leer = screen.getByRole("button", { name: /Leer/ });
    // "Neu" is empty, so the first stage with a task is "Wartet"; the running
    // task sits further on in "Läuft". Both are open, each for its own reason;
    // the empty stages stay shut.
    expect(neu).toHaveAttribute("aria-expanded", "false");
    expect(wartet).toHaveAttribute("aria-expanded", "true");
    expect(laeuft).toHaveAttribute("aria-expanded", "true");
    expect(leer).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("Aufgabe x1")).toBeVisible();
    expect(screen.getByText("Aufgabe x2")).toBeVisible();

    // A stage that started open and was shut by hand stays shut, also when
    // another stage is toggled afterwards.
    await userEvent.click(laeuft);
    expect(laeuft).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Aufgabe x2")).not.toBeVisible();
    await userEvent.click(leer);
    expect(leer).toHaveAttribute("aria-expanded", "true");
    expect(laeuft).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Aufgabe x2")).not.toBeVisible();
  });

  // The card is rendered inside the stage; shut, it is hidden and the mark
  // would run out behind the fold.
  it("öffnet die Stufe, in der eine neue Aufgabe landet, auch wenn sie zugeklappt war", async () => {
    const neu = aufgabe("x3", "s2", "open");
    const routes: Record<string, unknown> = {
      ...routen(),
      "/api/v1/agents/a1/stages": stufen,
      "/api/v1/agents/a1/backlog": [aufgabe("x1", "s1", "open")],
    };
    mockFetch(routes);
    const weiter = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === "POST" && String(input).endsWith("/api/v1/agents/a1/tasks")) {
          routes["/api/v1/agents/a1/backlog"] = [aufgabe("x1", "s1", "open"), neu];
          return new Response(JSON.stringify(neu), { status: 201, headers: { "Content-Type": "application/json" } });
        }
        return weiter(input, init);
      }),
    );
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    // "Neu" holds the first task and is open; "Wartet", where the new one lands, is shut.
    const wartet = await screen.findByRole("button", { name: /Wartet/ });
    expect(wartet).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(await screen.findByRole("button", { name: "Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), "Aufgabe x3{Enter}");

    expect(await screen.findByText("Aufgabe x3")).toBeVisible();
    expect(wartet).toHaveAttribute("aria-expanded", "true");
  });
});

// A task parked on "parked:<what>" is blocked on purpose; the board must tell
// it apart from one that waits for a ticket reply or an approval, and show
// the question the task was blocked with.
describe("Geparkte Aufgaben auf dem Board", () => {
  beforeEach(() => useGerman());

  const blocked = (over: Record<string, unknown>) => ({
    ...TASK,
    state: "blocked",
    title: "Kosten-Auswertung bauen",
    ...over,
  });

  async function zeige(task: Record<string, unknown>) {
    mockFetch({ ...routen(), "/api/v1/agents/a1/backlog": [task], "/api/v1/tasks/t1/notes": [] });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByText(task.title as string));
  }

  it("zeigt Badge „geparkt“, „geparkt bis …“ und den Grund", async () => {
    await zeige(
      blocked({
        correlation_key: "parked:feat/cost-insights",
        blocked_reason: "waits for branch ready: feat/cost-insights",
      }),
    );
    expect(screen.getByText("geparkt")).toBeInTheDocument();
    expect(screen.getByText(/geparkt bis feat\/cost-insights/)).toBeInTheDocument();
    expect(screen.queryByText(/wartet auf/)).not.toBeInTheDocument();
    expect(screen.getByText("waits for branch ready: feat/cost-insights")).toBeInTheDocument();
  });

  it("lässt andere Schlüssel bei „wartet“ und „wartet auf …“ und zeigt den Grund", async () => {
    await zeige(blocked({ correlation_key: "zammad:4711", blocked_reason: "Kunde muss antworten" }));
    expect(screen.queryByText("geparkt")).not.toBeInTheDocument();
    expect(screen.getByText("wartet")).toBeInTheDocument();
    expect(screen.getByText(/wartet auf zammad:4711/)).toBeInTheDocument();
    expect(screen.getByText("Kunde muss antworten")).toBeInTheDocument();
  });

  it("zeigt ohne Grund keine Grund-Zeile", async () => {
    await zeige(blocked({ correlation_key: "parked:feat/cost-insights" }));
    expect(screen.getByText("geparkt")).toBeInTheDocument();
    expect(screen.queryByText("Grund:")).not.toBeInTheDocument();
  });

  it("zeigt bei einem leeren Grund keine Grund-Zeile (anderer Schlüssel)", async () => {
    await zeige(blocked({ correlation_key: "zammad:4711", blocked_reason: "" }));
    expect(screen.queryByText("Grund:")).not.toBeInTheDocument();
  });

  // A redacted task keeps its text with the members of the conversation it
  // came from; the reason is the question it was blocked with, so it stays
  // there too.
  it("zeigt bei einer geschwärzten Aufgabe keinen Grund", async () => {
    const task = blocked({ correlation_key: "zammad:4711", blocked_reason: "Kunde muss antworten", redacted: true });
    mockFetch({ ...routen(), "/api/v1/agents/a1/backlog": [task], "/api/v1/tasks/t1/notes": [] });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByText("Aufgabe aus einem Gespräch"));
    expect(await screen.findByText(/in dem du nicht bist/)).toBeInTheDocument();
    expect(screen.queryByText("Grund:")).not.toBeInTheDocument();
    expect(screen.queryByText(/Kunde muss antworten/)).not.toBeInTheDocument();
  });
});

// The New task form sits behind a button: the board is what the tab is for,
// and a form that is always open pushes it down by a card's height on every
// visit. Closing the form keeps the draft — Escape is a way of looking at the
// board, not of throwing the text away.
describe("Neue Aufgabe hinter einem Knopf", () => {
  beforeEach(() => {
    useGerman();
    Element.prototype.scrollIntoView = vi.fn();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    // @ts-expect-error jsdom has no scrollIntoView; the test put one there.
    delete Element.prototype.scrollIntoView;
  });

  const NEU = { ...TASK, id: "t2", title: "Release-Notizen schreiben", state: "open", created_at: "2026-08-27T09:00:00Z" };
  const KNOPF = { name: "Neue Aufgabe" };

  // server answers the POST that creates a task: `post` returns the answer
  // (a promise lets a test hold it back), and `ctl.backlogDown` makes the
  // backlog's GET fail, as a refetch after the add may. Everything else goes
  // to the routes as usual.
  type Antwort = { status: number; body: unknown };
  function server(post: () => Antwort | Promise<Antwort>, routes: Record<string, unknown> = routen()) {
    mockFetch(routes);
    const weiter = globalThis.fetch;
    const posts: unknown[] = [];
    const ctl = { backlogDown: false };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (method === "POST" && url.endsWith("/api/v1/agents/a1/tasks")) {
          posts.push(JSON.parse(String(init?.body)));
          const antwort = await post();
          if (antwort.status < 300) routes["/api/v1/agents/a1/backlog"] = [NEU, TASK];
          return new Response(JSON.stringify(antwort.body), {
            status: antwort.status,
            headers: { "Content-Type": "application/json" },
          });
        }
        if (method === "GET" && ctl.backlogDown && url.includes("/api/v1/agents/a1/backlog")) {
          return new Response(JSON.stringify({ error: "down" }), { status: 500 });
        }
        return weiter(input, init);
      }),
    );
    return { posts, ctl };
  }
  const antwortet = (status: number, body: unknown) => () => ({ status, body });
  // held is a POST that stays out until the test lets it answer.
  function held() {
    let release: (a: Antwort) => void = () => {};
    const pending = new Promise<Antwort>((resolve) => (release = resolve));
    return { post: () => pending, release };
  }
  const titel = () => screen.getByPlaceholderText("Titel") as HTMLInputElement;
  const beschreibung = () => screen.getByPlaceholderText("Beschreibung / Kontext") as HTMLTextAreaElement;

  it("zeigt zu Beginn nur den Knopf, keine Felder", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    const knopf = await screen.findByRole("button", KNOPF);
    // A colour of its own, not the near-black primary (#15).
    expect(knopf.className).toContain("btn create");
    expect(knopf.className).not.toContain("primary");
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(screen.queryByPlaceholderText("Beschreibung / Kontext")).toBeNull();
  });

  it("zeigt ohne Verwaltungsrecht weder Knopf noch Formular", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage={false} onShowRecording={() => {}} />);

    expect(await screen.findByText(TASK.title)).toBeTruthy();
    expect(screen.queryByRole("button", KNOPF)).toBeNull();
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  it("zeigt den Knopf schon, während das Backlog noch lädt", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    expect(screen.getByRole("button", KNOPF)).toBeTruthy();
  });

  it("öffnet das Formular mit dem Fokus im Titel und sperrt den Knopf", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const knopf = await screen.findByRole("button", KNOPF);
    await userEvent.click(knopf);

    expect(document.activeElement).toBe(titel());
    expect(beschreibung()).toBeTruthy();
    expect((knopf as HTMLButtonElement).disabled).toBe(true);
  });

  it("verbindet Knopf, Formular und Titel für Hilfsmittel", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const knopf = await screen.findByRole("button", KNOPF);
    expect(knopf.getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(knopf);

    // The button names the form it opens; the label belongs to Title, the
    // description carries its own name, and the "+" is decoration.
    const form = titel().closest("form") as HTMLFormElement;
    expect(knopf.getAttribute("aria-controls")).toBe(form.id);
    expect(knopf.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByLabelText("Neue Aufgabe")).toBe(titel());
    expect(screen.getByLabelText("Beschreibung / Kontext")).toBe(beschreibung());
    expect(knopf.querySelector('[aria-hidden="true"]')?.textContent).toBe("+");
  });

  it("schließt mit Escape, behält den Entwurf und gibt dem Knopf den Fokus zurück", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), "Entwurf");
    await userEvent.type(beschreibung(), "mit Kontext");
    await userEvent.keyboard("{Escape}");

    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", KNOPF));
    await userEvent.click(screen.getByRole("button", KNOPF));
    expect(titel().value).toBe("Entwurf");
    expect(beschreibung().value).toBe("mit Kontext");
  });

  it("schließt mit Abbrechen, behält den Entwurf und gibt dem Knopf den Fokus zurück", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), "Entwurf");
    await userEvent.click(screen.getByRole("button", { name: "Abbrechen" }));

    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", KNOPF));
    await userEvent.click(screen.getByRole("button", KNOPF));
    expect(titel().value).toBe("Entwurf");
  });

  // Escape belongs to the form: with the focus on the search field it clears
  // the search, as that field does everywhere, and leaves the form alone.
  it("schließt das Formular nicht, wenn Escape außerhalb davon fällt", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), "Entwurf");
    await userEvent.click(screen.getByPlaceholderText("Backlog durchsuchen …"));
    await userEvent.keyboard("{Escape}");
    fireEvent.keyDown(document.body, { key: "Escape" });

    expect(titel().value).toBe("Entwurf");
  });

  // Escape during an input method's composition (ja, zh) cancels the
  // composition, not the form.
  it("lässt Escape während einer IME-Eingabe dem Eingabemodul", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    fireEvent.keyDown(titel(), { key: "Escape", isComposing: true });
    expect(titel()).toBeTruthy();

    fireEvent.keyDown(titel(), { key: "Escape" });
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  it("legt an, schließt, leert, gibt dem Knopf den Fokus und hebt die neue Karte hervor", async () => {
    const { posts } = server(antwortet(201, NEU));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), NEU.title);
    await userEvent.type(beschreibung(), "für 0.5");
    await userEvent.click(screen.getByRole("button", { name: "Ins Backlog" }));

    expect(posts).toEqual([{ title: NEU.title, body: "für 0.5" }]);
    const karte = (await screen.findByText(NEU.title)).closest(".kc") as HTMLElement;
    await waitFor(() => expect(karte.className).toContain("kc-new"));
    // The card is the one brought into view, not just any element.
    await waitFor(() => expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts).toContain(karte));
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", KNOPF));
    // Only the card the POST returned is marked, not its neighbours.
    expect((screen.getByText(TASK.title).closest(".kc") as HTMLElement).className).not.toContain("kc-new");

    await userEvent.click(screen.getByRole("button", KNOPF));
    expect(titel().value).toBe("");
    expect(beschreibung().value).toBe("");
  });

  it("nimmt die Hervorhebung nach etwa zwei Sekunden wieder weg", async () => {
    server(antwortet(201, NEU));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), NEU.title);
    await userEvent.keyboard("{Enter}");
    const karte = (await screen.findByText(NEU.title)).closest(".kc") as HTMLElement;
    await waitFor(() => expect(karte.className).toContain("kc-new"));

    await waitFor(() => expect(karte.className).not.toContain("kc-new"), { timeout: 3000 });
  });

  it("lässt das Formular bei einem Fehler offen und zeigt ihn", async () => {
    server(antwortet(500, { error: "backlog store unavailable" }));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), "Bleibt stehen");
    await userEvent.type(beschreibung(), "auch der Kontext");
    await userEvent.click(screen.getByRole("button", { name: "Ins Backlog" }));

    expect(await screen.findByText(/backlog store unavailable/)).toBeTruthy();
    expect(titel().value).toBe("Bleibt stehen");
    expect(beschreibung().value).toBe("auch der Kontext");
  });

  // Without the reset on closing, the old error stood in the form opened again.
  it("zeigt nach Escape und erneutem Öffnen den alten Fehler nicht mehr", async () => {
    server(antwortet(500, { error: "backlog store unavailable" }));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", KNOPF));
    await userEvent.type(titel(), "Bleibt stehen");
    await userEvent.click(screen.getByRole("button", { name: "Ins Backlog" }));
    expect(await screen.findByRole("alert")).toBeTruthy();

    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", KNOPF));
    expect(titel().value).toBe("Bleibt stehen");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  describe("während die Aufgabe unterwegs ist", () => {
    async function absenden(post: () => Promise<Antwort>) {
      const s = server(post);
      renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
      await userEvent.click(await screen.findByRole("button", KNOPF));
      // Enter in Title, so that the focus stays in the form where Escape is heard.
      await userEvent.type(titel(), `${NEU.title}{Enter}`);
      await waitFor(() => expect(s.posts).toHaveLength(1));
      return s;
    }

    // Reset while the POST is out would detach it: Add came back enabled on the
    // same draft, and a second press filed the task twice.
    it("ignoriert Escape und sperrt Abbrechen und Hinzufügen, es entsteht keine zweite Aufgabe", async () => {
      const h = held();
      const { posts } = await absenden(h.post);

      expect((screen.getByRole("button", { name: "Abbrechen" }) as HTMLButtonElement).disabled).toBe(true);
      await userEvent.keyboard("{Escape}");
      expect(titel().value).toBe(NEU.title);
      expect((screen.getByRole("button", { name: "Ins Backlog" }) as HTMLButtonElement).disabled).toBe(true);
      await userEvent.keyboard("{Enter}");
      expect(posts).toHaveLength(1);

      h.release({ status: 201, body: NEU });
      await waitFor(() => expect(screen.queryByPlaceholderText("Titel")).toBeNull());
      expect(await screen.findByText(NEU.title)).toBeTruthy();
    });

    it("zeigt einen späten Fehler im offen gebliebenen Formular", async () => {
      const h = held();
      await absenden(h.post);
      await userEvent.keyboard("{Escape}");

      h.release({ status: 500, body: { error: "backlog store unavailable" } });
      expect(await screen.findByText(/backlog store unavailable/)).toBeTruthy();
      expect(titel().value).toBe(NEU.title);
      expect((screen.getByRole("button", { name: "Abbrechen" }) as HTMLButtonElement).disabled).toBe(false);
    });

    // The POST is slow and the reader has gone to the search: the answer must
    // not pull the focus back to the button under their typing.
    it("nimmt dem Leser den Fokus nicht, wenn die Antwort spät kommt", async () => {
      const h = held();
      await absenden(h.post);
      const suche = screen.getByPlaceholderText("Backlog durchsuchen …");
      await userEvent.click(suche);
      expect(document.activeElement).toBe(suche);

      h.release({ status: 201, body: NEU });
      await waitFor(() => expect(screen.queryByPlaceholderText("Titel")).toBeNull());
      expect(document.activeElement).toBe(suche);
    });
  });

  // The router keeps the Backlog mounted when the page moves from one agent to
  // another (Back, a search hit). What was typed for one is not filed on the
  // other.
  describe("beim Wechsel des Agenten", () => {
    function Wechsel() {
      const [id, setId] = useState("a1");
      return (
        <>
          <button onClick={() => setId("a2")}>zu a2</button>
          <Backlog agentId={id} canManage onShowRecording={() => {}} />
        </>
      );
    }

    it("bleibt weder Entwurf noch offenes Formular stehen", async () => {
      mockFetch({
        ...routen(),
        "/api/v1/agents/a2/backlog": [{ ...TASK, id: "t9", agent_id: "a2", title: "Aufgabe von a2" }],
        "/api/v1/agents/a2/stages": [],
      });
      renderWithProviders(<Wechsel />);
      await userEvent.click(await screen.findByRole("button", KNOPF));
      await userEvent.type(titel(), "Entwurf für a1");
      await userEvent.type(screen.getByPlaceholderText("Backlog durchsuchen …"), "x");

      await userEvent.click(screen.getByRole("button", { name: "zu a2" }));
      expect(await screen.findByText("Aufgabe von a2")).toBeTruthy();
      expect(screen.queryByPlaceholderText("Titel")).toBeNull();
      expect((screen.getByPlaceholderText("Backlog durchsuchen …") as HTMLInputElement).value).toBe("");
      await userEvent.click(screen.getByRole("button", KNOPF));
      expect(titel().value).toBe("");
    });
  });

  describe("die Hervorhebung der neuen Karte", () => {
    // Fake timers that still move on their own, so that react-query and the
    // user-event pauses keep running; the test steps the clock where it matters.
    async function neueKarte(ctl: { backlogDown: boolean } | null = null) {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
      await user.click(await screen.findByRole("button", KNOPF));
      await user.type(titel(), NEU.title);
      if (ctl) ctl.backlogDown = true;
      await user.click(screen.getByRole("button", { name: "Ins Backlog" }));
      return user;
    }

    // The board is not on screen during a search, and its hits carry no mark.
    // The two seconds must not run out behind it.
    it("läuft nicht ab, solange eine Suche das Board verdeckt", async () => {
      server(antwortet(201, NEU));
      const user = await neueKarte();
      await user.type(screen.getByPlaceholderText("Backlog durchsuchen …"), "Release");
      await waitFor(() => expect(document.querySelector(".kanban")).toBeNull());
      await vi.advanceTimersByTimeAsync(3000);
      await user.clear(screen.getByPlaceholderText("Backlog durchsuchen …"));

      // Asked afresh each time: the hit of the search is a card too, and goes
      // when the board comes back.
      await waitFor(() => expect(screen.getByText(NEU.title).closest(".kc")?.className).toContain("kc-new"));
    });

    // The refetch after the add failed: the card never showed up, and the mark
    // waited for it. Flashing on some later refetch would mark a card that has
    // been on the board for minutes.
    it("verfällt, wenn die Karte nie ankommt", async () => {
      const { ctl } = server(antwortet(201, NEU));
      const user = await neueKarte(ctl);
      await waitFor(() => expect(screen.queryByPlaceholderText("Titel")).toBeNull());
      await vi.advanceTimersByTimeAsync(11000);

      ctl.backlogDown = false;
      await user.click(screen.getByRole("button", { name: "Archiv anzeigen" }));
      await user.click(await screen.findByRole("button", { name: "Archiv ausblenden" }));
      const karte = (await screen.findByText(NEU.title)).closest(".kc") as HTMLElement;
      expect(karte.className).not.toContain("kc-new");
    });
  });
});

// "n" opens the New task form on the Backlog tab, the way "/" reaches the
// search on the dashboard. It is a key for whoever is looking at the board, so
// it stays out of the way of anyone typing and of the browser's own Ctrl/Cmd+N.
describe("Taste n öffnet Neue Aufgabe", () => {
  beforeEach(() => useGerman());

  const KNOPF = { name: "Neue Aufgabe" };
  const taste = (init: KeyboardEventInit, target: Element = document.body) => {
    const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
    act(() => {
      target.dispatchEvent(ev);
    });
    return ev;
  };

  it("öffnet das Formular mit dem Fokus im Titel, ohne das n hineinzuschreiben", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const knopf = await screen.findByRole("button", KNOPF);
    expect(knopf.title).toBe("Neue Aufgabe (N)");
    expect(knopf.getAttribute("aria-keyshortcuts")).toBe("N");
    await userEvent.keyboard("n");

    const titel = screen.getByPlaceholderText("Titel") as HTMLInputElement;
    expect(document.activeElement).toBe(titel);
    expect(titel.value).toBe("");
  });

  // The tooltip takes the button's own words: no second string per locale to
  // keep in step with "New task".
  it("nennt die Taste im Tooltip mit dem Wort des Knopfes", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const knopf = await screen.findByRole("button", KNOPF);
    expect(knopf.title).toBe(`${knopf.textContent?.replace("+ ", "")} (N)`);
  });

  it("öffnet nichts, solange in ein Feld geschrieben wird", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const suche = await screen.findByPlaceholderText("Backlog durchsuchen …");
    await userEvent.type(suche, "n");

    expect((suche as HTMLInputElement).value).toBe("n");
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();

    // Neither in a field of the form itself: an n in the description is text.
    await userEvent.click(screen.getByRole("button", KNOPF));
    const beschreibung = screen.getByPlaceholderText("Beschreibung / Kontext") as HTMLTextAreaElement;
    await userEvent.type(beschreibung, "n");
    expect(beschreibung.value).toBe("n");
    expect(document.activeElement).toBe(beschreibung);
  });

  // Nor in an editable element outside a form field (the chat's composer).
  it("öffnet nichts in einem editierbaren Element", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);
    const editierbar = document.createElement("div");
    Object.defineProperty(editierbar, "isContentEditable", { value: true });
    document.body.appendChild(editierbar);
    try {
      expect(taste({ key: "n" }, editierbar).defaultPrevented).toBe(false);
      expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    } finally {
      editierbar.remove();
    }
  });

  it("lässt Strg+N, Cmd+N und Alt+N dem Browser", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);
    for (const mod of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }]) {
      expect(taste({ key: "n", ...mod }).defaultPrevented).toBe(false);
    }
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  it("tut ohne Verwaltungsrecht nichts", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage={false} onShowRecording={() => {}} />);
    await screen.findByText(TASK.title);

    expect(taste({ key: "n" }).defaultPrevented).toBe(false);
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  // The tab unmounts the Backlog when it is left; the key goes with it. A
  // listener left behind would open a form nobody sees and, once the tab is
  // back, would answer twice — it would take the key (preventDefault) on a page
  // that has no form to give it to.
  it("nimmt die Taste mit, wenn der Reiter verlassen wird", async () => {
    mockFetch(routen());
    const { unmount } = renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);
    expect(taste({ key: "n" }).defaultPrevented).toBe(true);
    unmount();

    expect(taste({ key: "n" }).defaultPrevented).toBe(false);
  });

  describe("hinter einem offenen Dialog", () => {
    it("öffnet nichts, solange ein modaler Dialog offen ist", async () => {
      mockFetch(routen());
      renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
      await screen.findByRole("button", KNOPF);
      const dialog = document.createElement("div");
      dialog.setAttribute("aria-modal", "true");
      document.body.appendChild(dialog);
      try {
        expect(taste({ key: "n" }).defaultPrevented).toBe(false);
        expect(screen.queryByPlaceholderText("Titel")).toBeNull();
      } finally {
        dialog.remove();
      }
      expect(taste({ key: "n" }).defaultPrevented).toBe(true);
    });

    // A drawer that is shut stays in the DOM as role=dialog, inert (the console
    // navigation at phone width): it is not in front of anything.
    it("zählt einen geschlossenen, inerten Dialog nicht", async () => {
      mockFetch(routen());
      renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
      await screen.findByRole("button", KNOPF);
      const dialog = document.createElement("nav");
      dialog.setAttribute("role", "dialog");
      dialog.setAttribute("inert", "");
      document.body.appendChild(dialog);
      try {
        expect(taste({ key: "n" }).defaultPrevented).toBe(true);
        expect(await screen.findByPlaceholderText("Titel")).toBeTruthy();
      } finally {
        dialog.remove();
      }
    });

    // The help drawer is role=dialog without aria-modal and leaves the focus
    // outside: "?" then "n" opened the form behind the scrim.
    it("öffnet nichts hinter der Hilfe", async () => {
      mockFetch(routen());
      renderWithProviders(
        <>
          <HelpDrawer open onClose={() => {}} />
          <Backlog agentId="a1" canManage onShowRecording={() => {}} />
        </>,
      );
      await screen.findByRole("button", KNOPF);

      expect(taste({ key: "n" }).defaultPrevented).toBe(false);
      expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    });
  });

  // Held down, the key repeats into whatever has the focus — and once the form
  // opened, that is Title: "nnn".
  it("schreibt eine gehaltene Taste nicht in den Titel", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);
    expect(taste({ key: "n" }).defaultPrevented).toBe(true);
    const titel = screen.getByPlaceholderText("Titel") as HTMLInputElement;
    expect(document.activeElement).toBe(titel);

    expect(taste({ key: "n", repeat: true }, titel).defaultPrevented).toBe(true);
    // Let go, and an n is an n again.
    window.dispatchEvent(new KeyboardEvent("keyup", { key: "n", bubbles: true }));
    expect(taste({ key: "n", repeat: true }, titel).defaultPrevented).toBe(false);
    await userEvent.type(titel, "n");
    expect(titel.value).toBe("n");
  });

  it("öffnet bei einer wiederholten Taste nichts", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);

    expect(taste({ key: "n", repeat: true }).defaultPrevented).toBe(false);
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  // Where the layout makes no Latin letter of it, the key at the N position
  // counts; where another Latin letter stands there (Dvorak), it does not.
  it("gilt auch auf Layouts ohne lateinisches n, nicht aber auf Dvorak", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);

    expect(taste({ key: "b", code: "KeyN" }).defaultPrevented).toBe(false);
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(taste({ key: "т", code: "KeyN" }).defaultPrevented).toBe(true);
    expect(screen.getByPlaceholderText("Titel")).toBeTruthy();
  });

  it("lässt eine Taste während einer IME-Eingabe in Ruhe", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await screen.findByRole("button", KNOPF);

    expect(taste({ key: "n", isComposing: true }).defaultPrevented).toBe(false);
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });
});
