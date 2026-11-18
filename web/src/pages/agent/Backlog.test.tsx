import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ladeSprache } from "../../i18n";
import type { Stage } from "../../api";
import { Backlog, stageLabel } from "./Backlog";
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

// The three default columns are found by key, not by name: the board shows
// them in the chosen language, and a switch of language relabels them without
// asking the server again. A column a human renamed keeps that name in every
// language; every other column shows its name as it is. The columns below are
// what GET /agents/{id}/stages answers (TestFreshBoardIsKeyed and
// TestRenamedDoneColumnKeepsAutoFollow read the same fields off the server):
// a default column nobody renamed carries its old German name, renamed false.
describe("Spalten des Boards", () => {
  const STAGES = [
    { id: "s1", agent_id: "a1", name: "Backlog", key: "open", renamed: false, position: 0, color: "var(--text-muted)", created_by: "seed", created_at: "2026-01-01T00:00:00Z" },
    { id: "s2", agent_id: "a1", name: "In Arbeit", key: "in_progress", renamed: false, position: 1, color: "", created_by: "seed", created_at: "2026-01-01T00:00:00Z" },
    { id: "s3", agent_id: "a1", name: "Shipped", key: "done", renamed: true, position: 2, color: "", created_by: "seed", created_at: "2026-01-01T00:00:00Z" },
    { id: "s4", agent_id: "a1", name: "Research", key: null, renamed: false, position: 3, color: "", created_by: "agent", created_at: "2026-01-01T00:00:00Z" },
  ];
  afterEach(() => useGerman());
  const spalten = () => Array.from(document.querySelectorAll(".kanban .knm")).map((n) => n.textContent);

  it("zeigt die Standardspalten in der gewählten Sprache und wechselt mit ihr", async () => {
    useGerman();
    const { calls } = mockFetch({
      ...routen(),
      "/api/v1/agents/a1/backlog": [{ ...TASK, stage_id: "s3" }],
      "/api/v1/agents/a1/stages": STAGES,
    });
    const stageFetches = () => calls.filter((c) => c.includes("/stages")).length;
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    expect(await screen.findByText("In Arbeit")).toBeTruthy();
    expect(spalten()).toEqual(["Backlog", "In Arbeit", "Shipped", "Research"]);

    const vorher = stageFetches();
    await act(() => ladeSprache("en"));
    expect(await screen.findByText("In progress")).toBeTruthy();
    expect(spalten()).toEqual(["Backlog", "In progress", "Shipped", "Research"]);
    await act(() => ladeSprache("fr"));
    expect(await screen.findByText("En cours")).toBeTruthy();
    expect(spalten()).toEqual(["Backlog", "En cours", "Shipped", "Research"]);
    // Only the language changed — nothing was fetched again.
    expect(stageFetches()).toBe(vorher);
  });

  // What a human saw: a running task in a column "Done" beside an empty
  // "Erledigt". The server no longer makes that second column (set_stage
  // "Done" lands in the keyed one); the board shows the keyed done column
  // under the label of the language, and the running task in it once.
  it("zeigt eine laufende Aufgabe in genau einer Spalte, der Standardspalte für erledigt", async () => {
    await act(() => ladeSprache("en"));
    const done = { ...STAGES[2], name: "Done", renamed: false };
    mockFetch({
      ...routen(),
      "/api/v1/agents/a1/backlog": [{ ...TASK, state: "in_progress", stage_id: "s3" }],
      "/api/v1/agents/a1/stages": [STAGES[0], STAGES[1], done],
    });
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    expect(await screen.findByText(TASK.title)).toBeTruthy();
    expect(spalten()).toEqual(["Backlog", "In progress", "Done"]);
    const karten = Array.from(document.querySelectorAll(".kanban .kcol")).map(
      (col) => col.querySelectorAll(".kh .n")[0]?.textContent,
    );
    expect(karten).toEqual(["0", "0", "1"]);
    expect(screen.getAllByText(TASK.title)).toHaveLength(1);
  });

  it("nennt eine unbenannte Standardspalte so, wie die Sprache sie nennt", () => {
    const t = ((key: string) => `«${key}»`) as unknown as Parameters<typeof stageLabel>[1];
    expect(stageLabel(STAGES[0] as Stage, t)).toBe("«agent.backlog.defaultStage.open»");
    expect(stageLabel(STAGES[2] as Stage, t)).toBe("Shipped");
    expect(stageLabel(STAGES[3] as Stage, t)).toBe("Research");
    // A server from before the key sends neither key nor renamed.
    expect(stageLabel({ ...STAGES[3], key: undefined, renamed: undefined, name: "Erledigt" } as Stage, t)).toBe("Erledigt");
  });

  // What the column editor sends: the stored name on a color change, the
  // typed text as it is, and default_name when the label of the chosen
  // language is typed over a renamed default column.
  const patches = () =>
    vi
      .mocked(fetch)
      .mock.calls.filter(([, init]) => init?.method === "PATCH")
      .map(([url, init]) => ({ url: String(url), body: JSON.parse(String(init?.body)) }));
  const editor = async () => {
    renderWithProviders(<Backlog agentId="a1" canManage onShowRecording={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit columns" }));
    const card = screen.getByText("Columns (Stages)").closest(".card") as HTMLElement;
    return Array.from(card.querySelectorAll("input")).slice(0, STAGES.length);
  };
  const retype = async (field: HTMLInputElement, text: string) => {
    await userEvent.clear(field);
    await userEvent.type(field, text);
    await act(async () => field.blur());
  };

  it("sendet beim Umfärben den gespeicherten Namen, nicht die Beschriftung", async () => {
    await act(() => ladeSprache("en"));
    mockFetch({ ...routen(), "/api/v1/agents/a1/stages": STAGES, "PATCH /api/v1/stages/s2": STAGES[1] });
    await editor();
    // "In progress" on screen, "In Arbeit" stored: the color change keeps it.
    await userEvent.click(screen.getAllByTitle("Change color")[1]);
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]).toMatchObject({ url: "/api/v1/stages/s2", body: { name: "In Arbeit" } });
    expect(patches()[0].body.default_name).toBeUndefined();
  });

  it("speichert Getipptes wie getippt und nimmt die Beschriftung der Sprache als Rücknahme", async () => {
    await act(() => ladeSprache("en"));
    mockFetch({
      ...routen(),
      "/api/v1/agents/a1/stages": STAGES,
      "PATCH /api/v1/stages/s2": STAGES[1],
      "PATCH /api/v1/stages/s3": STAGES[2],
    });
    const fields = await editor();
    expect(fields.map((f) => f.value)).toEqual(["Backlog", "In progress", "Shipped", "Research"]);
    // Another language's label is a name like any other.
    await retype(fields[1], "En cours");
    // The English label over the renamed done column takes the rename back.
    await retype(fields[2], "Done");
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[0]).toMatchObject({ url: "/api/v1/stages/s2", body: { name: "En cours" } });
    expect(patches()[0].body.default_name).toBeUndefined();
    expect(patches()[1]).toMatchObject({ url: "/api/v1/stages/s3", body: { default_name: true } });
  });

  it("sagt, wenn der Server einen Namen ablehnt", async () => {
    await act(() => ladeSprache("en"));
    mockFetch({ ...routen(), "/api/v1/agents/a1/stages": STAGES });
    const base = globalThis.fetch;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) =>
      init?.method === "PATCH"
        ? new Response(JSON.stringify({ error: "the board already has a column of that name or meaning" }), { status: 409 })
        : base(input, init),
    );
    const fields = await editor();
    await retype(fields[3], "Done");
    expect(await screen.findByText("This board already has a column with that name or meaning.")).toBeInTheDocument();
  });
});
