package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
)

// The cost page groups its agent filter by department, so the cells name
// each agent's department: two agents in two departments carry theirs, an
// agent without one carries null for both, over the API as in the store.
func TestCostCellsNameTheAgentsDepartment(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), Ord: 0,
		SecretKey: "claude_code_oauth_token", SecretSlot: 0, Kind: "subscription"}

	dept := func(name string) uuid.UUID {
		var id uuid.UUID
		if err := s.pool.QueryRow(ctx, `INSERT INTO departments (org_id, name) VALUES ($1, $2) RETURNING id`, s.orgID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	finance, sales := dept("Finance"), dept("Sales")
	in := map[string]uuid.UUID{}
	for slug, d := range map[string]*uuid.UUID{"kosten-finanzen": &finance, "kosten-vertrieb": &sales, "kosten-ohne": nil} {
		agent := s.newSupportAgent(slug)
		in[slug] = agent.ID
		if d != nil {
			if _, err := s.pool.Exec(ctx, `UPDATE agents SET department_id = $2 WHERE id = $1`, agent.ID, *d); err != nil {
				t.Fatal(err)
			}
		}
		task, err := s.backlog.Create(ctx, agent.OrgID, agent.ID, "Eine Aufgabe", "", "heartbeat", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.obs.BookCost(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task.ID, USD: 1.0,
			Model: opus, Credential: seat}); err != nil {
			t.Fatal(err)
		}
	}
	want := map[uuid.UUID]struct {
		id   *uuid.UUID
		name string
	}{
		in["kosten-finanzen"]: {&finance, "Finance"},
		in["kosten-vertrieb"]: {&sales, "Sales"},
		in["kosten-ohne"]:     {nil, ""},
	}

	cells, err := s.obs.CostCellsFor(ctx, s.orgID, "hour", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.Agents) != 3 {
		t.Fatalf("agents = %+v, want the three booked ones", cells.Agents)
	}
	for _, a := range cells.Agents {
		w := want[a.ID]
		if w.id == nil {
			if a.DepartmentID != nil || a.Department != nil {
				t.Errorf("agent without a department: %v / %v, want both nil", a.DepartmentID, a.Department)
			}
			continue
		}
		if a.DepartmentID == nil || *a.DepartmentID != *w.id || a.Department == nil || *a.Department != w.name {
			t.Errorf("agent %s: department %v / %v, want %s / %s", a.ID, a.DepartmentID, a.Department, w.id, w.name)
		}
	}

	got := admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)
	list, _ := got["agents"].([]any)
	if len(list) != 3 {
		t.Fatalf("GET /cost/cells agents = %v", got["agents"])
	}
	for _, x := range list {
		a := x.(map[string]any)
		id, _ := uuid.Parse(a["id"].(string))
		w := want[id]
		depID, hasID := a["department_id"]
		dep, hasName := a["department"]
		if !hasID || !hasName {
			t.Errorf("GET /cost/cells agent %v lacks department_id or department", a)
			continue
		}
		if w.id == nil {
			if depID != nil || dep != nil {
				t.Errorf("GET /cost/cells agent without a department: %v / %v, want null / null", depID, dep)
			}
			continue
		}
		if depID != w.id.String() || dep != w.name {
			t.Errorf("GET /cost/cells agent %s: %v / %v, want %s / %s", id, depID, dep, w.id, w.name)
		}
	}
}

// A department of another organisation is never named in the cells, even if
// an agent's row pointed at one.
func TestCostCellsNeverNameAForeignDepartment(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	seat := observability.CredentialRef{RuntimeID: uuid.New(), Ord: 0,
		SecretKey: "claude_code_oauth_token", SecretSlot: 0, Kind: "subscription"}
	other, foreign := uuid.New(), uuid.UUID{}
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, 'Other Ltd')`, other); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `INSERT INTO departments (org_id, name) VALUES ($1, 'Elsewhere') RETURNING id`, other).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	agent := s.newSupportAgent("kosten-fremd")
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET department_id = $2 WHERE id = $1`, agent.ID, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookCost(ctx, observability.CostEntry{AgentID: agent.ID, USD: 1.0,
		Model: opus, Credential: seat}); err != nil {
		t.Fatal(err)
	}
	cells, err := s.obs.CostCellsFor(ctx, s.orgID, "hour", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.Agents) != 1 || cells.Agents[0].DepartmentID != nil || cells.Agents[0].Department != nil {
		t.Errorf("agents = %+v, want the one agent without a department", cells.Agents)
	}
}
