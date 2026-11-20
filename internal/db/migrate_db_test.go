package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
// both, and apply nothing — not even the migrations below the colliding one,
// which a check inside the apply loop would let through.
func TestMigrateUpRefusesAVersionAppliedUnderAnotherName(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 142, "0142_other")
	fsys := fstest.MapFS{
		"0141_before.up.sql":     {Data: []byte("CREATE TABLE before_probe (id INT)")},
		"0142_stage_keys.up.sql": {Data: []byte("CREATE TABLE stage_keys_probe (id INT)")},
		"0143_after.up.sql":      {Data: []byte("CREATE TABLE after_probe (id INT)")},
	}

	n, err := MigrateUp(context.Background(), pool, fsys)
	if err == nil {
		t.Fatal("version 142 applied as 0142_other was skipped without an error")
	}
	var mm *NameMismatchError
	if !errors.As(err, &mm) || len(mm.Mismatches) != 1 {
		t.Fatalf("error is %T, expected one *NameMismatchError: %v", err, err)
	}
	for _, want := range []string{"0142_stage_keys", "0142_other", "renumber"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	// The remedy names a rule, not the file on disk: that file may be main's,
	// and other databases have already run it.
	if strings.Contains(err.Error(), "renumber 0142_stage_keys") {
		t.Errorf("the remedy tells to renumber the file on disk: %v", err)
	}
	if mm.Mismatches[0].AppliedAt.IsZero() {
		t.Error("the error carries no applied_at")
	}
	if n != 0 {
		t.Errorf("applied = %d, expected 0", n)
	}
	for _, table := range []string{"before_probe", "stage_keys_probe", "after_probe"} {
		if tableExists(t, pool, table) {
			t.Errorf("%s exists: a migration ran although the run was refused", table)
		}
	}
}

// Renumbering on merge is the usual way two branches resolve a collision
// (0085 → 0087). A database that ran the branch build has the old number, so by
// version the renumbered file is new and would run a second time. Both inputs
// are refused before anything runs, and the refusal after the first one is
// "fixed" still stands — the advice must not lead to a double run.
func TestMigrateUpRefusesAMigrationRenumberedAfterItRan(t *testing.T) {
	const applied = "0085_registration_token_expiry"
	const renumbered = "0087_registration_token_expiry"
	probe := []byte("CREATE TABLE rte_probe (id INT)")

	cases := []struct {
		name  string
		fsys  fstest.MapFS
		wants []string // fragments the error must contain
		count int      // mismatches expected
	}{
		{"another file took the old number",
			fstest.MapFS{
				"0085_other.up.sql":    {Data: []byte("CREATE TABLE other_probe (id INT)")},
				renumbered + ".up.sql": {Data: probe},
			}, []string{"0085_other", applied, renumbered, "second time"}, 2},
		{"after that file moved away",
			fstest.MapFS{
				renumbered + ".up.sql": {Data: probe},
				"0152_other.up.sql":    {Data: []byte("CREATE TABLE other_probe (id INT)")},
			}, []string{applied, renumbered, "second time"}, 1},
		{"no other file at all",
			fstest.MapFS{renumbered + ".up.sql": {Data: probe}},
			[]string{applied, renumbered, "second time"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := testPool(t)
			recordApplied(t, pool, 85, applied)
			n, err := MigrateUp(context.Background(), pool, c.fsys)
			var mm *NameMismatchError
			if !errors.As(err, &mm) {
				t.Fatalf("MigrateUp returned %v, expected a *NameMismatchError", err)
			}
			if len(mm.Mismatches) != c.count {
				t.Errorf("%d mismatches, expected %d: %v", len(mm.Mismatches), c.count, err)
			}
			for _, want := range c.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not name %q: %v", want, err)
				}
			}
			if n != 0 || tableExists(t, pool, "rte_probe") || tableExists(t, pool, "other_probe") {
				t.Errorf("a migration ran although the run was refused (applied = %d)", n)
			}
			// Pending says the same before a restart does.
			if _, err := Pending(context.Background(), pool, c.fsys); !errors.As(err, &mm) {
				t.Errorf("Pending returned %v, expected a *NameMismatchError", err)
			}
		})
	}
}

// The way out for a database that ran a branch build is one row: record the
// migration under the number main gave it. The statement the doctor prints is
// run here as it is printed; after it the rest runs, and the moved migration
// does not run a second time.
func TestMigrateUpAfterTheRowIsMovedToMainsNumber(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	recordApplied(t, pool, 84, "0084_base")
	recordApplied(t, pool, 85, "0085_rte")
	// Main files only: the database's own numbering for rte is on no file.
	fsys := fstest.MapFS{
		"0084_base.up.sql": {Data: []byte("CREATE TABLE base_probe (id INT)")},
		"0085_a.up.sql":    {Data: []byte("CREATE TABLE a_probe (id INT)")},
		"0086_b.up.sql":    {Data: []byte("CREATE TABLE b_probe (id INT)")},
		"0087_rte.up.sql":  {Data: []byte("CREATE TABLE rte_probe (id INT)")},
	}

	_, err := MigrateUp(ctx, pool, fsys)
	var mm *NameMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("MigrateUp returned %v, expected a *NameMismatchError", err)
	}
	var stmt string
	for _, m := range mm.Mismatches {
		if m.Renumbered() {
			stmt = m.Statement()
		} else if m.Statement() != "" {
			t.Errorf("a same-number mismatch prints a statement: %q", m.Statement())
		}
	}
	const want = "UPDATE schema_migrations SET version=87, name='0087_rte' WHERE version=85"
	if stmt != want {
		t.Fatalf("statement = %q, expected %q", stmt, want)
	}

	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatal(err)
	}
	n, err := MigrateUp(ctx, pool, fsys)
	if err != nil {
		t.Fatalf("MigrateUp after the row was moved: %v", err)
	}
	if n != 2 {
		t.Errorf("applied = %d, expected the two new ones (0085_a, 0086_b)", n)
	}
	for _, table := range []string{"a_probe", "b_probe"} {
		if !tableExists(t, pool, table) {
			t.Errorf("%s was not created: the rest did not run", table)
		}
	}
	if tableExists(t, pool, "rte_probe") {
		t.Error("the moved migration ran a second time")
	}
}

// Two mismatches are both reported at once, in file order, so that fixing the
// first does not reveal the second only at the next start.
func TestNameMismatchErrorListsEveryMismatch(t *testing.T) {
	applied := map[int]appliedMigration{
		1: {name: "0001_x"},
		2: {name: "0002_y"},
	}
	ms := []migration{{version: 1, name: "0001_a"}, {version: 2, name: "0002_b"}}
	_, err := unapplied(ms, applied)
	var mm *NameMismatchError
	if !errors.As(err, &mm) || len(mm.Mismatches) != 2 {
		t.Fatalf("expected two mismatches, got %v", err)
	}
	if mm.Mismatches[0].File != "0001_a" || mm.Mismatches[1].File != "0002_b" {
		t.Errorf("mismatches = %+v", mm.Mismatches)
	}
}

// A new migration that merely reuses the name of one that is still in the tree
// is a new migration, not a renumbered one.
func TestMigrateUpRunsANewMigrationWithARepeatedName(t *testing.T) {
	pool := testPool(t)
	recordApplied(t, pool, 1, "0001_add_index")
	fsys := fstest.MapFS{
		"0001_add_index.up.sql": {Data: []byte("CREATE TABLE first_probe (id INT)")},
		"0002_add_index.up.sql": {Data: []byte("CREATE TABLE second_probe (id INT)")},
	}
	n, err := MigrateUp(context.Background(), pool, fsys)
	if err != nil || n != 1 || !tableExists(t, pool, "second_probe") || tableExists(t, pool, "first_probe") {
		t.Fatalf("applied = %d, err = %v", n, err)
	}
}

// Only "no such table" means a database that was never migrated.
func TestIsUndefinedTable(t *testing.T) {
	if !isUndefinedTable(fmt.Errorf("read: %w", &pgconn.PgError{Code: "42P01", Message: `relation "schema_migrations" does not exist`})) {
		t.Error("42P01 is not recognised")
	}
	denied := &pgconn.PgError{Code: "42501", Message: "permission denied for table schema_migrations"}
	if isUndefinedTable(denied) {
		t.Error("a permission error reads as a database that was never migrated")
	}
	if isUndefinedTable(errors.New(`relation "schema_migrations" does not exist`)) {
		t.Error("a plain error with the table in its text counts as undefined_table")
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
	if len(mm.Mismatches) != 1 {
		t.Fatalf("mismatches = %+v", mm.Mismatches)
	}
	if m := mm.Mismatches[0]; m.File != "0142_stage_keys" || m.Applied != "0142_other" || m.Version != 142 || m.Renumbered() {
		t.Errorf("mismatch = %+v", m)
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
