package db

import (
	"context"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool hands out a fresh, empty database on the test Postgres and drops it
// afterwards. Without a Postgres the test is skipped, as in the doctor's tests.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("COVEY_TEST_DATABASE_URL")
	if admin == "" {
		admin = "postgres://covey:covey@localhost:5433/covey?sslmode=disable"
	}
	ctx := context.Background()
	adminPool, err := Connect(ctx, admin)
	if err != nil {
		t.Skipf("no test Postgres at %s: %v", admin, err)
	}
	t.Cleanup(adminPool.Close)
	name := "covey_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adminPool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })

	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// recordApplied writes a schema_migrations row the way an earlier binary did.
func recordApplied(t *testing.T, pool *pgxpool.Pool, version int, name string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1,$2)", version, name); err != nil {
		t.Fatal(err)
	}
}

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// The deployed database applied 0142 under one name; a branch that was numbered
// against an older main ships 0142 under another. MigrateUp used to see
// "142 applied" and skip the file, silently and for good. It must refuse, name
// both, and apply nothing — not even the migrations behind the colliding one.
func TestMigrateUpRefusesAVersionAppliedUnderAnotherName(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 142, "0142_other")
	fsys := fstest.MapFS{
		"0142_stage_keys.up.sql": {Data: []byte("CREATE TABLE stage_keys_probe (id INT)")},
		"0143_after.up.sql":      {Data: []byte("CREATE TABLE after_probe (id INT)")},
	}

	n, err := MigrateUp(context.Background(), pool, fsys)
	if err == nil {
		t.Fatal("version 142 applied as 0142_other was skipped without an error")
	}
	var mm *NameMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("error is %T, expected *NameMismatchError: %v", err, err)
	}
	for _, want := range []string{"0142_stage_keys", "0142_other", "renumber"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if mm.AppliedAt.IsZero() {
		t.Error("the error carries no applied_at")
	}
	if n != 0 {
		t.Errorf("applied = %d, expected 0", n)
	}
	if tableExists(t, pool, "after_probe") || tableExists(t, pool, "stage_keys_probe") {
		t.Error("a migration ran although the run was refused")
	}
}

// The ordinary case stays as it was: same version and same name is skipped, the
// rest runs.
func TestMigrateUpSkipsAVersionAppliedUnderTheSameName(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 1, "0001_a")
	fsys := fstest.MapFS{
		"0001_a.up.sql": {Data: []byte("CREATE TABLE a_probe (id INT)")},
		"0002_b.up.sql": {Data: []byte("CREATE TABLE b_probe (id INT)")},
	}
	n, err := MigrateUp(context.Background(), pool, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("applied = %d, expected 1", n)
	}
	if tableExists(t, pool, "a_probe") {
		t.Error("0001_a ran again although it was applied")
	}
	if !tableExists(t, pool, "b_probe") {
		t.Error("0002_b did not run")
	}
}

// A row with no file — a database ahead of the binary — is left as it is today.
func TestMigrateUpIgnoresAnAppliedRowWithoutAFile(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 9, "0009_from_a_newer_binary")
	fsys := fstest.MapFS{"0001_a.up.sql": {Data: []byte("CREATE TABLE a_probe (id INT)")}}
	if _, err := MigrateUp(context.Background(), pool, fsys); err != nil {
		t.Fatalf("a database ahead of the binary was refused: %v", err)
	}
}

// Pending used to compare against MAX(version): a migration that slid in below
// the highest applied one was not listed, so the doctor said "up to date" about
// a database MigrateUp would still change.
func TestPendingListsAMigrationBelowTheHighestApplied(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 1, "0001_a")
	recordApplied(t, pool, 3, "0003_c")
	fsys := fstest.MapFS{
		"0001_a.up.sql": {Data: []byte("SELECT 1")},
		"0002_b.up.sql": {Data: []byte("SELECT 1")},
		"0003_c.up.sql": {Data: []byte("SELECT 1")},
		"0004_d.up.sql": {Data: []byte("SELECT 1")},
	}
	got, err := Pending(context.Background(), pool, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0002_b", "0004_d"}; !slices.Equal(got, want) {
		t.Errorf("Pending = %v, expected %v", got, want)
	}
}

// Pending reports the same refusal MigrateUp gives, so that `covey doctor`
// shows it before a restart does.
func TestPendingReportsANameMismatch(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 142, "0142_other")
	fsys := fstest.MapFS{"0142_stage_keys.up.sql": {Data: []byte("SELECT 1")}}
	_, err := Pending(context.Background(), pool, fsys)
	var mm *NameMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("Pending returned %v, expected a *NameMismatchError", err)
	}
	if mm.File != "0142_stage_keys" || mm.Applied != "0142_other" || mm.Version != 142 {
		t.Errorf("mismatch = %+v", mm)
	}
}

// An unmigrated database has no table: everything is pending, not an error.
func TestPendingOnAnUnmigratedDatabase(t *testing.T) {
	pool := testPool(t)
	fsys := fstest.MapFS{"0001_a.up.sql": {Data: []byte("SELECT 1")}}
	got, err := Pending(context.Background(), pool, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"0001_a"}) {
		t.Errorf("Pending = %v", got)
	}
}
