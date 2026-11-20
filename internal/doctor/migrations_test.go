package doctor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"covey/internal/config"
	"covey/internal/db"
	"covey/migrations"
)

// migratedDatabase is a throwaway database with every embedded migration
// applied; it skips without a test Postgres.
func migratedDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	admin := os.Getenv("COVEY_TEST_DATABASE_URL")
	if admin == "" {
		admin = "postgres://covey:covey@localhost:5433/covey?sslmode=disable"
	}
	ctx := context.Background()
	adminPool, err := db.Connect(ctx, admin)
	if err != nil {
		t.Skipf("no test Postgres at %s: %v", admin, err)
	}
	t.Cleanup(adminPool.Close)
	name := "covey_doctor_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adminPool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.MigrateUp(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool, u.String()
}

// A migration number applied under another name makes `covey serve` refuse to
// start. The doctor is what somebody runs to find out why, so it has to say the
// same thing — and as a blocking finding, not as "pending", whose remedy is a
// backup and a restart that would only hit the same refusal.
func TestDoctorReportsAMigrationAppliedUnderAnotherName(t *testing.T) {
	ctx := context.Background()
	pool, dbURL := migratedDatabase(t)
	var file string
	if err := pool.QueryRow(ctx, "SELECT name FROM schema_migrations WHERE version = 1").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE schema_migrations SET name = '0001_other' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}

	rep := RunWith(ctx, config.Config{DatabaseURL: dbURL}, pool)
	var found bool
	for _, f := range rep.Findings {
		if f.What != "migrations" {
			continue
		}
		found = true
		if !f.Blocking {
			t.Error("a refused migration is not reported as blocking")
		}
		for _, want := range []string{file, "0001_other"} {
			if !strings.Contains(f.Detail, want) {
				t.Errorf("the finding does not name %q: %q", want, f.Detail)
			}
		}
		// Worded by who can act. This shape has no row to move, so there is no
		// statement, and a row main never got is not the operator's to settle.
		for _, want := range []string{"unmerged branch", "backup", "developer's decision"} {
			if !strings.Contains(f.Remedy, want) {
				t.Errorf("the remedy does not say %q: %q", want, f.Remedy)
			}
		}
		if strings.Contains(f.Remedy, "UPDATE") {
			t.Errorf("the remedy prints a statement for a row with nothing to move to: %q", f.Remedy)
		}
	}
	// "start covey serve once" would be wrong advice: serve refuses.
	for _, f := range rep.Findings {
		if f.What == "the rest" && (strings.Contains(f.Detail, "start `covey serve`") || !strings.Contains(f.Detail, "numbering")) {
			t.Errorf("the rest contradicts the refusal: %q", f.Detail)
		}
	}
	if !found {
		t.Fatalf("the doctor says nothing about the migrations: %+v", rep.Findings)
	}
}

// A database that ran a branch build records a migration under the number the
// branch gave it; main renumbered it on merge. The doctor prints the one-row
// update that moves the record, for a person to run — it changes nothing itself.
func TestDoctorPrintsTheStatementForARenumberedMigration(t *testing.T) {
	ctx := context.Background()
	pool, dbURL := migratedDatabase(t)
	var version int
	var file string
	if err := pool.QueryRow(ctx, "SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version, &file); err != nil {
		t.Fatal(err)
	}
	// The branch build ran it as 9000, under a number main has no file for.
	_, slug, _ := strings.Cut(file, "_")
	branchName := "9000_" + slug
	if _, err := pool.Exec(ctx, "UPDATE schema_migrations SET version = 9000, name = $1 WHERE version = $2", branchName, version); err != nil {
		t.Fatal(err)
	}

	rep := RunWith(ctx, config.Config{DatabaseURL: dbURL}, pool)
	var found bool
	for _, f := range rep.Findings {
		if f.What != "migrations" {
			continue
		}
		found = true
		if !f.Blocking {
			t.Error("a refused migration is not reported as blocking")
		}
		want := fmt.Sprintf("UPDATE schema_migrations SET version=%d, name='%s' WHERE version=9000", version, file)
		if !strings.Contains(f.Remedy, want) {
			t.Errorf("the remedy does not print %q: %q", want, f.Remedy)
		}
		for _, want := range []string{"backup", "git diff"} {
			if !strings.Contains(f.Remedy, want) {
				t.Errorf("the remedy does not say %q: %q", want, f.Remedy)
			}
		}
	}
	if !found {
		t.Fatalf("the doctor says nothing about the migrations: %+v", rep.Findings)
	}
	// Reading changed nothing: the row is still where it was.
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 9000").Scan(&n); err != nil || n != 1 {
		t.Errorf("the doctor touched schema_migrations (rows at 9000: %d, err %v)", n, err)
	}
}
