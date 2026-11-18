import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { screen } from "@testing-library/react";
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
    const neu = screen.getByRole("button", { name: /Neu/ });
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
});
