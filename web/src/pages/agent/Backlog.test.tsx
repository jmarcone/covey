import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Backlog } from "./Backlog";
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

// The New task form sits behind a button: the board is what the tab is for,
// and a form that is always open pushes it down by a card's height on every
// visit. Closing the form keeps the draft — Escape is a way of looking at the
// board, not of throwing the text away.
describe("Neue Aufgabe hinter einem Knopf", () => {
  beforeEach(() => {
    useGerman();
    Element.prototype.scrollIntoView = vi.fn();
  });

  const NEU = { ...TASK, id: "t2", title: "Release-Notizen schreiben", state: "open", created_at: "2026-08-27T09:00:00Z" };

  // mitPost answers the POST that creates a task: with the task, or with an
  // error. Everything else goes to the routes as usual.
  function mitPost(antwort: { status: number; body: unknown }, routes: Record<string, unknown> = routen()) {
    mockFetch(routes);
    const weiter = globalThis.fetch;
    const posts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if ((init?.method ?? "GET") === "POST" && url.endsWith("/api/v1/agents/a1/tasks")) {
          posts.push(JSON.parse(String(init?.body)));
          if (antwort.status < 300) routes["/api/v1/agents/a1/backlog"] = [NEU, TASK];
          return new Response(JSON.stringify(antwort.body), {
            status: antwort.status,
            headers: { "Content-Type": "application/json" },
          });
        }
        return weiter(input, init);
      }),
    );
    return posts;
  }

  it("zeigt zu Beginn nur den Knopf, keine Felder", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    const knopf = await screen.findByRole("button", { name: "+ Neue Aufgabe" });
    expect(knopf.className).toContain("btn primary");
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    expect(screen.queryByPlaceholderText("Beschreibung / Kontext")).toBeNull();
  });

  it("zeigt ohne Verwaltungsrecht weder Knopf noch Formular", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage={false} onShowRecording={() => {}} />);

    expect(await screen.findByText(TASK.title)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "+ Neue Aufgabe" })).toBeNull();
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
  });

  it("zeigt den Knopf schon, während das Backlog noch lädt", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);

    expect(screen.getByRole("button", { name: "+ Neue Aufgabe" })).toBeTruthy();
  });

  it("öffnet das Formular mit dem Fokus im Titel und sperrt den Knopf", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    const knopf = await screen.findByRole("button", { name: "+ Neue Aufgabe" });
    await userEvent.click(knopf);

    expect(document.activeElement).toBe(screen.getByPlaceholderText("Titel"));
    expect(screen.getByPlaceholderText("Beschreibung / Kontext")).toBeTruthy();
    expect((knopf as HTMLButtonElement).disabled).toBe(true);
  });

  it("schließt mit Escape und behält den Entwurf", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), "Entwurf");
    await userEvent.type(screen.getByPlaceholderText("Beschreibung / Kontext"), "mit Kontext");
    await userEvent.keyboard("{Escape}");

    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "+ Neue Aufgabe" }));
    expect((screen.getByPlaceholderText("Titel") as HTMLInputElement).value).toBe("Entwurf");
    expect((screen.getByPlaceholderText("Beschreibung / Kontext") as HTMLTextAreaElement).value).toBe("mit Kontext");
  });

  it("schließt mit Abbrechen und behält den Entwurf", async () => {
    mockFetch(routen());
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), "Entwurf");
    await userEvent.click(screen.getByRole("button", { name: "Abbrechen" }));

    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "+ Neue Aufgabe" }));
    expect((screen.getByPlaceholderText("Titel") as HTMLInputElement).value).toBe("Entwurf");
  });

  it("legt an, schließt, leert und hebt die neue Karte hervor", async () => {
    const posts = mitPost({ status: 201, body: NEU });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), NEU.title);
    await userEvent.type(screen.getByPlaceholderText("Beschreibung / Kontext"), "für 0.5");
    await userEvent.click(screen.getByRole("button", { name: "Ins Backlog" }));

    expect(posts).toEqual([{ title: NEU.title, body: "für 0.5" }]);
    const karte = (await screen.findByText(NEU.title)).closest(".kc") as HTMLElement;
    await waitFor(() => expect(karte.className).toContain("kc-new"));
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    expect(screen.queryByPlaceholderText("Titel")).toBeNull();
    // Only the card the POST returned is marked, not its neighbours.
    expect((screen.getByText(TASK.title).closest(".kc") as HTMLElement).className).not.toContain("kc-new");

    await userEvent.click(screen.getByRole("button", { name: "+ Neue Aufgabe" }));
    expect((screen.getByPlaceholderText("Titel") as HTMLInputElement).value).toBe("");
    expect((screen.getByPlaceholderText("Beschreibung / Kontext") as HTMLTextAreaElement).value).toBe("");
  });

  it("nimmt die Hervorhebung nach etwa zwei Sekunden wieder weg", async () => {
    mitPost({ status: 201, body: NEU });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), NEU.title);
    await userEvent.keyboard("{Enter}");
    const karte = (await screen.findByText(NEU.title)).closest(".kc") as HTMLElement;
    await waitFor(() => expect(karte.className).toContain("kc-new"));

    await waitFor(() => expect(karte.className).not.toContain("kc-new"), { timeout: 3000 });
  });

  it("lässt das Formular bei einem Fehler offen und zeigt ihn", async () => {
    mitPost({ status: 500, body: { error: "backlog store unavailable" } });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ Neue Aufgabe" }));
    await userEvent.type(screen.getByPlaceholderText("Titel"), "Bleibt stehen");
    await userEvent.type(screen.getByPlaceholderText("Beschreibung / Kontext"), "auch der Kontext");
    await userEvent.click(screen.getByRole("button", { name: "Ins Backlog" }));

    expect(await screen.findByText(/backlog store unavailable/)).toBeTruthy();
    expect((screen.getByPlaceholderText("Titel") as HTMLInputElement).value).toBe("Bleibt stehen");
    expect((screen.getByPlaceholderText("Beschreibung / Kontext") as HTMLTextAreaElement).value).toBe("auch der Kontext");
  });
});
