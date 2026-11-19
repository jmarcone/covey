import { describe, it, expect, afterEach, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ladeSprache } from "../../i18n";
import type { Stage } from "../../api";
import { Backlog, stageLabel } from "./Backlog";
import { mockFetch, renderWithProviders, useGerman } from "../../test/render";

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

const routen = () => ({
  "/api/v1/agents/a1/backlog": [TASK],
  "/api/v1/agents/a1/stages": [],
  "/api/v1/tasks/t1/notes": [],
  "/api/v1/org": ORG,
  "/api/v1/version": BUILD,
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
    const done = { ...STAGES[2], name: "Erledigt", renamed: false };
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

  // After a refusal the field shows the stored name again, not the refused
  // text; and the message speaks of the last change only — a refused "Add"
  // is gone once a rename goes through.
  it("setzt ein abgelehntes Feld zurück und sagt die Ablehnung nur bis zur nächsten Änderung", async () => {
    await act(() => ladeSprache("en"));
    mockFetch({ ...routen(), "/api/v1/agents/a1/stages": STAGES, "PATCH /api/v1/stages/s2": STAGES[1] });
    const base = globalThis.fetch;
    const refusal = () =>
      new Response(JSON.stringify({ error: "the board already has a column of that name or meaning" }), { status: 409 });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) =>
        (init?.method === "PATCH" && String(input).endsWith("/stages/s4")) || init?.method === "POST"
          ? refusal()
          : base(input, init),
      ),
    );
    const message = "This board already has a column with that name or meaning.";
    const fields = await editor();
    await retype(fields[3], "Done");
    expect(await screen.findByText(message)).toBeInTheDocument();
    const card = screen.getByText("Columns (Stages)").closest(".card") as HTMLElement;
    await waitFor(() => expect(card.querySelectorAll("input")[3]).toHaveValue("Research"));

    await userEvent.type(within(card).getByPlaceholderText("New column…"), "Done");
    await userEvent.click(within(card).getByRole("button", { name: "Add" }));
    expect(await screen.findByText(message)).toBeInTheDocument();
    await retype(card.querySelectorAll("input")[1] as HTMLInputElement, "En cours");
    await waitFor(() => expect(patches().some((p) => p.url === "/api/v1/stages/s2")).toBe(true));
    await waitFor(() => expect(screen.queryByText(message)).not.toBeInTheDocument());
  });
});
