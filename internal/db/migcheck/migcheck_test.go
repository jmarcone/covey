package migcheck

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func mig(file string) Migration {
	m, err := Parse(file)
	if err != nil {
		panic(err)
	}
	return m
}

func onMain(file string) Other {
	return Other{Migration: mig(file), Source: Source{Kind: SourceMain}}
}

func inPR(file string, pr int, branch string) Other {
	return Other{Migration: mig(file), Source: Source{Kind: SourcePR, PR: pr, Branch: branch}}
}

func TestParse(t *testing.T) {
	m, err := Parse("migrations/0142_stage_keys.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := Migration{Version: 142, Name: "0142_stage_keys", File: "migrations/0142_stage_keys.up.sql"}
	if m != want {
		t.Fatalf("got %+v, want %+v", m, want)
	}

	// The down file and anything else is skipped, not an error to report.
	for _, f := range []string{"migrations/0142_stage_keys.down.sql", "migrations/README.md", "0142_x.sql"} {
		if _, err := Parse(f); !errors.Is(err, ErrNotUp) {
			t.Errorf("Parse(%q): err = %v, want ErrNotUp", f, err)
		}
	}

	// A file that claims to be a migration and is not one stays an error.
	for _, f := range []string{"migrations/stage_keys.up.sql", "migrations/0142.up.sql", "migrations/0142_.up.sql", "migrations/-1_x.up.sql", "migrations/+142_x.up.sql", "migrations/_x.up.sql"} {
		_, err := Parse(f)
		if err == nil || errors.Is(err, ErrNotUp) {
			t.Errorf("Parse(%q): err = %v, want a malformed-name error", f, err)
		}
	}
}

// The four cases of the acceptance criteria, plus what comes with them.
func TestCompare(t *testing.T) {
	probe := mig("migrations/0142_probe.up.sql")
	stageKeys := "migrations/0142_stage_keys.up.sql"

	tests := []struct {
		name   string
		added  []Migration
		others []Other
		want   []Finding
	}{
		{
			name:   "1: main already has the version under another name",
			added:  []Migration{probe},
			others: []Other{onMain("migrations/0141_org_sso_last_error.up.sql"), onMain(stageKeys)},
			want:   []Finding{{Kind: Collision, Added: probe, Other: onMain(stageKeys)}},
		},
		{
			name:   "2: another open PR adds the version under another name",
			added:  []Migration{probe},
			others: []Other{onMain("migrations/0141_org_sso_last_error.up.sql"), inPR(stageKeys, 2, "fix/stage-keys")},
			want:   []Finding{{Kind: Collision, Added: probe, Other: inPR(stageKeys, 2, "fix/stage-keys")}},
		},
		{
			name:   "3: same version and same name in another PR is no collision",
			added:  []Migration{mig(stageKeys)},
			others: []Other{inPR(stageKeys, 2, "fix/stage-keys")},
			want:   []Finding{{Kind: SameName, Added: mig(stageKeys), Other: inPR(stageKeys, 2, "fix/stage-keys")}},
		},
		{
			name:   "4: a PR that adds no migration has nothing to collide",
			added:  nil,
			others: []Other{onMain(stageKeys), inPR("migrations/0143_x.up.sql", 3, "x")},
			want:   nil,
		},
		{
			name:   "a free number is no finding",
			added:  []Migration{mig("migrations/0149_new.up.sql")},
			others: []Other{onMain(stageKeys), inPR("migrations/0147_a.up.sql", 2, "a")},
			want:   nil,
		},
		{
			name:   "the number matches as a number, not as text",
			added:  []Migration{mig("migrations/00142_probe.up.sql")},
			others: []Other{onMain(stageKeys)},
			want:   []Finding{{Kind: Collision, Added: mig("migrations/00142_probe.up.sql"), Other: onMain(stageKeys)}},
		},
		{
			name:  "one added file collides with main and with a PR, a second one with nothing",
			added: []Migration{probe, mig("migrations/0150_other.up.sql")},
			others: []Other{
				onMain(stageKeys),
				inPR("migrations/0142_notes.up.sql", 7, "fix/notes"),
				inPR("migrations/0143_z.up.sql", 8, "z"),
			},
			want: []Finding{
				{Kind: Collision, Added: probe, Other: onMain(stageKeys)},
				{Kind: Collision, Added: probe, Other: inPR("migrations/0142_notes.up.sql", 7, "fix/notes")},
			},
		},
		{
			name:  "a stacked PR repeats its base's migration and adds one of its own that collides",
			added: []Migration{mig(stageKeys), probe},
			others: []Other{
				inPR(stageKeys, 2, "fix/stage-keys"),
			},
			want: []Finding{
				{Kind: SameName, Added: mig(stageKeys), Other: inPR(stageKeys, 2, "fix/stage-keys")},
				{Kind: Collision, Added: probe, Other: inPR(stageKeys, 2, "fix/stage-keys")},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compare(tc.added, tc.others)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Compare:\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

func TestBlocking(t *testing.T) {
	probe := mig("migrations/0142_probe.up.sql")
	same := mig("migrations/0143_same.up.sql")
	fs := Compare([]Migration{probe, same}, []Other{
		onMain("migrations/0142_stage_keys.up.sql"),
		inPR("migrations/0143_same.up.sql", 4, "b"),
	})
	if len(fs) != 2 {
		t.Fatalf("got %d findings, want 2", len(fs))
	}
	got := Blocking(fs)
	if len(got) != 1 || got[0].Added != probe {
		t.Fatalf("Blocking = %+v, want only the collision", got)
	}
	if Blocking(nil) != nil {
		t.Fatal("Blocking(nil) should be nil")
	}
}

// What a person reads: both files, and where the other one lives.
func TestFindingString(t *testing.T) {
	probe := mig("migrations/0142_probe.up.sql")
	stage := "migrations/0142_stage_keys.up.sql"
	applied := Other{
		Migration: Migration{Version: 142, Name: "0142_cost_insights"},
		Source:    Source{Kind: SourceApplied, AppliedAt: time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)},
	}
	tests := []struct {
		f    Finding
		want string
	}{
		{
			Finding{Kind: Collision, Added: probe, Other: onMain(stage)},
			"migrations/0142_probe.up.sql collides with migrations/0142_stage_keys.up.sql in main — both take version 0142 under different names",
		},
		{
			Finding{Kind: Collision, Added: probe, Other: inPR(stage, 2, "fix/stage-keys")},
			"migrations/0142_probe.up.sql collides with migrations/0142_stage_keys.up.sql in PR #2 (fix/stage-keys) — both take version 0142 under different names",
		},
		{
			Finding{Kind: SameName, Added: mig(stage), Other: inPR(stage, 2, "fix/stage-keys")},
			"migrations/0142_stage_keys.up.sql carries version 0142 under the same name as migrations/0142_stage_keys.up.sql in PR #2 (fix/stage-keys) — not a collision",
		},
		// The deployed-database check has no file on the other side, only a row.
		{
			Finding{Kind: Collision, Added: probe, Other: applied},
			"migrations/0142_probe.up.sql collides with 0142_cost_insights in the deployed database (applied 2026-11-17T09:30:00Z) — both take version 0142 under different names",
		},
	}
	for _, tc := range tests {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("String:\n got  %s\n want %s", got, tc.want)
		}
	}
}
