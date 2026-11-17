import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { act, screen } from "@testing-library/react";
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

// The three default columns are stored by key, not by name: the board shows
// them in the chosen language, and a switch of language relabels them without
// asking the server again. A column a human renamed keeps that name in every
// language; every other column shows its name as it is.
describe("Spalten des Boards", () => {
  const STAGES = [
    { id: "s1", agent_id: "a1", name: "Backlog", key: "open", renamed: false, position: 0, color: "", created_by: "human", created_at: "2026-01-01T00:00:00Z" },
    { id: "s2", agent_id: "a1", name: "In progress", key: "in_progress", renamed: false, position: 1, color: "", created_by: "human", created_at: "2026-01-01T00:00:00Z" },
    { id: "s3", agent_id: "a1", name: "Shipped", key: "done", renamed: true, position: 2, color: "", created_by: "human", created_at: "2026-01-01T00:00:00Z" },
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
});
