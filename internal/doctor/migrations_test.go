package doctor

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"covey/internal/config"
	"covey/internal/db"
	"covey/migrations"
)

// A migration number applied under another name makes `covey serve` refuse to
// start. The doctor is what somebody runs to find out why, so it has to say the
// same thing — and as a blocking finding, not as "pending", whose remedy is a
// backup and a restart that would only hit the same refusal.
func TestDoctorReportsAMigrationAppliedUnderAnotherName(t *testing.T) {
	admin := os.Getenv("COVEY_TEST_DATABASE_URL")
	if admin == "" {
		admin = "postgres://covey:covey@localhost:5433/covey?sslmode=disable"
	}
	ctx := context.Background()
	adminPool, err := db.Connect(ctx, admin)
	if err != nil {
		t.Skipf("no test Postgres at %s: %v", admin, err)
	}
	defer adminPool.Close()
	name := "covey_doctor_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer adminPool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := db.MigrateUp(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var file string
	if err := pool.QueryRow(ctx, "SELECT name FROM schema_migrations WHERE version = 1").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE schema_migrations SET name = '0001_other' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}

	rep := RunWith(ctx, config.Config{DatabaseURL: u.String()}, pool)
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
		// The reader is an operator: no "renumber <file>", which they cannot do.
		if strings.Contains(f.Remedy, "renumber") || !strings.Contains(f.Remedy, "keep running") {
			t.Errorf("the remedy is not worded for an operator: %q", f.Remedy)
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
