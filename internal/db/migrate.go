package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes migrations across instances (HA start-up).
const advisoryLockKey = 74_65_79 // "covey"

type migration struct {
	version int
	name    string
	upSQL   string
	downSQL string
}

// loadMigrations reads NNNN_name.up.sql / NNNN_name.down.sql from the FS.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*migration{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		base, isUp := strings.CutSuffix(name, ".up.sql")
		if !isUp {
			var isDown bool
			base, isDown = strings.CutSuffix(name, ".down.sql")
			if !isDown {
				return nil, fmt.Errorf("migration %s: expected .up.sql or .down.sql", name)
			}
		}
		versionStr, _, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: expected NNNN_name", name)
		}
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			return nil, fmt.Errorf("migration %s: version: %w", name, err)
		}
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		m := byVersion[version]
		if m == nil {
			m = &migration{version: version, name: base}
			byVersion[version] = m
		}
		// Two different migrations under the same number — that happens as soon as
		// two branches assign the same number in parallel. Until now the
		// alphabetically later file silently won: its SQL overwrote the other's,
		// only one version was recorded, and the lost migration never ran. The
		// damage shows up only as a missing column in live operation.
		// Fail-closed: better that the control plane does not start at all than
		// that it starts with half a schema.
		if m.name != base {
			a, b := m.name, base
			if a > b {
				a, b = b, a
			}
			return nil, fmt.Errorf("migrations %s and %s share version %d — renumber one of them", a, b, version)
		}
		if isUp {
			m.upSQL = string(content)
		} else {
			m.downSQL = string(content)
		}
	}
	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.upSQL == "" {
			return nil, fmt.Errorf("migration %d (%s): up.sql missing", m.version, m.name)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// NameMismatch is one disagreement between a migration file and the
// schema_migrations rows of a database. There are two shapes, and both end in a
// migration that is skipped or run twice:
//
//   - the same number under another name (Applied != File, AppliedVersion ==
//     Version): the file is skipped silently and forever, because
//     MigrateUp sees "version applied";
//   - the same migration under another number (Renumbered): a branch build ran
//     it as 0085, main renumbered it to 0087 on merge. By version the file is
//     new, and it would run a second time.
//
// Two refinements of those, for rows that cannot simply be moved:
//
//   - RecordedAs: the row of the first shape is for a migration main carries
//     under another number, and that number already has its own row. The row to
//     move has nowhere to go; it is the stale one.
//   - Candidates: the row of the second shape fits more than one file. Which one
//     the database ran is not something the names can say.
type NameMismatch struct {
	Version        int       // the number of the file on disk
	File           string    // the name on disk
	AppliedVersion int       // the number the database recorded
	Applied        string    // the name the database recorded
	AppliedAt      time.Time // when that row was written
	RecordedAs     string    // the file the row's migration is already recorded as, if any
	Candidates     []string  // every file that fits the row, when more than one does
}

// Ambiguous reports that the row fits more than one file.
func (m NameMismatch) Ambiguous() bool { return len(m.Candidates) > 1 }

// Renumbered reports the second shape: the database ran this migration, under
// another number.
func (m NameMismatch) Renumbered() bool { return m.Version != m.AppliedVersion }

// String names both sides. It does not say which side is the right one: the
// guard cannot tell a database that ran a branch build from a binary that is
// the branch, and it cannot tell the same migration from a different one with
// the same name.
func (m NameMismatch) String() string {
	at := m.AppliedAt.UTC().Format(time.RFC3339)
	if m.Ambiguous() {
		return fmt.Sprintf("this database carries a migration of the same name, %s, as version %d (applied at %s); this binary has %s — ambiguous: check which one the database ran",
			m.Applied, m.AppliedVersion, at, strings.Join(m.Candidates, " and "))
	}
	if m.Renumbered() {
		return fmt.Sprintf("this database carries a migration of the same name, %s, as version %d (applied at %s); this binary has it as %s under version %d. If it is the same migration, it would run a second time",
			m.Applied, m.AppliedVersion, at, m.File, m.Version)
	}
	s := fmt.Sprintf("this database applied version %d as %s (at %s), from another build; this binary carries %s under the same number",
		m.Version, m.Applied, at, m.File)
	if m.RecordedAs != "" {
		s += fmt.Sprintf("; that migration is already recorded as %s", m.RecordedAs)
	}
	return s
}

// Statement is the one-row change that settles the row, or empty where the
// names cannot say what to do:
//
//   - a renumbered migration: the update that records it under the number this
//     binary carries it as;
//   - a row whose migration is already recorded under main's number (RecordedAs):
//     the delete of the stale row — an update would collide with the row that is
//     already there;
//   - a row that fits several files, or a number applied under another name with
//     nothing to move the row to: none, which side is wrong is a decision.
//
// It is printed for a person to run, after a backup and after they have checked
// that the SQL of the two files is the same; nothing here runs it.
func (m NameMismatch) Statement() string {
	if m.Ambiguous() {
		return ""
	}
	if m.RecordedAs != "" {
		return fmt.Sprintf("DELETE FROM schema_migrations WHERE version=%d", m.AppliedVersion)
	}
	if !m.Renumbered() {
		return ""
	}
	return fmt.Sprintf("UPDATE schema_migrations SET version=%d, name='%s' WHERE version=%d",
		m.Version, strings.ReplaceAll(m.File, "'", "''"), m.AppliedVersion)
}

// NameMismatchError says that migration files and the database disagree on a
// number or a name. loadMigrations refuses two files with one number in one
// tree; this is the same rule against the database. Every mismatch is listed,
// so that fixing the first does not reveal the second only at the next start.
type NameMismatchError struct {
	Mismatches []NameMismatch
}

// NameMismatchRemedy is what to do about it, worded by who can act, because
// which side is wrong depends on which build ran against the database:
//
//   - the binary is an unmerged branch: its migration is renumbered in the
//     source (CLAUDE.md);
//   - the database ran a branch build whose numbering main changed: the operator
//     records the row under main's number — after a backup, and after checking
//     that the SQL is the same;
//   - a row of a migration main never got is a developer's decision.
const NameMismatchRemedy = "if this binary is an unmerged branch, renumber its migration in the source (CLAUDE.md); if the database ran a branch build whose numbering main changed, record the row under main's number after a backup and after checking that the SQL is the same (`covey doctor` prints the statement); a row of a migration main never got is a developer's decision. `covey serve` refuses until rows and files agree"

func (e *NameMismatchError) Error() string {
	lines := make([]string, len(e.Mismatches))
	for i, m := range e.Mismatches {
		lines[i] = m.String()
	}
	return strings.Join(lines, "\n") + "\n" + NameMismatchRemedy
}

type appliedMigration struct {
	name      string
	appliedAt time.Time
}

// appliedMigrations reads schema_migrations per version, not as a water mark —
// see MigrateUp for why a MAX(version) is the wrong question.
func appliedMigrations(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[int]appliedMigration, error) {
	rows, err := q.Query(ctx, "SELECT version, name, applied_at FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]appliedMigration{}
	for rows.Next() {
		var v int
		var a appliedMigration
		if err := rows.Scan(&v, &a.name, &a.appliedAt); err != nil {
			return nil, err
		}
		out[v] = a
	}
	return out, rows.Err()
}

// slug is a migration name without its number: "0087_stage_keys" → "stage_keys".
// A migration renumbered on merge keeps it.
func slug(name string) string {
	_, rest, ok := strings.Cut(name, "_")
	if !ok {
		return name
	}
	return rest
}

// isUndefinedTable is Postgres's "no such table" (42P01), matched by code: the
// text of other errors mentions the table too ("permission denied for table
// schema_migrations").
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// unapplied returns the migrations this database has not seen, and refuses —
// listing every case at once — when files and rows disagree about a name:
//
//   - a version the database has seen carries another name than the file;
//   - a file the database has not seen by version has the name of a row whose own
//     file is gone (same name after the number, another number). That is a
//     migration renumbered after the database ran it; without this it runs a
//     second time. A new file that merely repeats the name of a migration still
//     in the tree is not that, and is left alone.
//
// One row that several unapplied files fit is one mismatch naming every
// candidate. A same-number mismatch whose migration is already recorded under
// the number of another file says so (RecordedAs).
//
// A database row without any file (a database ahead of the binary) is left alone.
func unapplied(ms []migration, applied map[int]appliedMigration) ([]migration, error) {
	onDisk := make(map[string]bool, len(ms))
	for _, m := range ms {
		onDisk[m.name] = true
	}
	// Rows whose name no file carries, by slug — the lowest version wins, so the
	// message does not depend on map order.
	versions := make([]int, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	orphans := map[string]int{}
	for _, v := range versions {
		if onDisk[applied[v].name] {
			continue
		}
		if _, dup := orphans[slug(applied[v].name)]; !dup {
			orphans[slug(applied[v].name)] = v
		}
	}

	var out []migration
	var bad []NameMismatch
	// The mismatch a row's slug already has, so that a second file of that slug
	// joins it as a candidate instead of moving the same row again.
	moved := map[string]int{}
	for _, m := range ms {
		a, ok := applied[m.version]
		switch {
		case ok && a.name != m.name:
			bad = append(bad, NameMismatch{Version: m.version, File: m.name, AppliedVersion: m.version, Applied: a.name, AppliedAt: a.appliedAt,
				RecordedAs: recordedAs(ms, applied, m, a)})
		case ok:
		default:
			v, orphan := orphans[slug(m.name)]
			if !orphan {
				out = append(out, m)
				continue
			}
			if i, seen := moved[slug(m.name)]; seen {
				c := &bad[i]
				if c.Candidates == nil {
					c.Candidates = []string{c.File}
				}
				c.Candidates = append(c.Candidates, m.name)
				continue
			}
			moved[slug(m.name)] = len(bad)
			bad = append(bad, NameMismatch{Version: m.version, File: m.name, AppliedVersion: v, Applied: applied[v].name, AppliedAt: applied[v].appliedAt})
		}
	}
	if len(bad) > 0 {
		return nil, &NameMismatchError{Mismatches: bad}
	}
	return out, nil
}

// recordedAs is the file that the migration of a mismatched row is already
// recorded as: another file with the row's name after the number, whose own
// version carries its own name in the database. The row at m's number is then
// the stale one — this is what a build from before the guard leaves, when it
// skipped m by version and ran the same migration again under main's number.
func recordedAs(ms []migration, applied map[int]appliedMigration, m migration, row appliedMigration) string {
	for _, f := range ms {
		if f.version == m.version || slug(f.name) != slug(row.name) {
			continue
		}
		if a, ok := applied[f.version]; ok && a.name == f.name {
			return f.name
		}
	}
	return ""
}

// Pending names the migrations this database has not seen yet — read, without
// applying anything. It exists so that an operator can ask what a restart is
// about to do to their database BEFORE they restart, which is the moment they
// would want to have a backup. It asks the question MigrateUp asks, version by
// version, and returns a *NameMismatchError where MigrateUp would refuse.
func Pending(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	all, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	// A database that has never been migrated has no table yet — and then
	// everything is pending, which is the right answer and not an error.
	applied, err := appliedMigrations(ctx, pool)
	if err != nil {
		// Only "no such table" means never migrated. Any other error — a
		// permission denied on the table, say — is not an empty database.
		if !isUndefinedTable(err) {
			return nil, err
		}
		applied = nil
	}
	todo, err := unapplied(all, applied)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range todo {
		out = append(out, m.name)
	}
	return out, nil
}

// MigrateUp applies all pending migrations, guarded by pg_advisory_lock.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (applied int, err error) {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return 0, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return 0, err
	}
	// Keep the applied versions INDIVIDUALLY, not as a water mark. A
	// `version <= MAX(applied)` skips every migration that slides in below the
	// highest version already applied — and that is exactly what happens on a
	// merge: two branches hand out their numbers against the same main, the
	// second merges with the smaller one. Skipped silently and forever, because
	// on the next start the mark is unchanged and still as high.
	//
	// The name counts too: a version applied under another name, or a name applied
	// under another version, is the same collision one step later (the branch
	// merged after the other one ran). It is refused before anything is applied,
	// like two files with one number.
	seen, err := appliedMigrations(ctx, conn)
	if err != nil {
		return 0, err
	}
	todo, err := unapplied(ms, seen)
	if err != nil {
		return 0, err
	}
	for _, m := range todo {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, m.upSQL); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1,$2)", m.version, m.name); err != nil {
			tx.Rollback(ctx)
			return applied, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// MigrateDown rolls back exactly one migration (emergency; normal operation is forward-only).
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)

	var current int
	if err := conn.QueryRow(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current); err != nil {
		return err
	}
	if current == 0 {
		return fmt.Errorf("no migration applied")
	}
	for _, m := range ms {
		if m.version != current {
			continue
		}
		if m.downSQL == "" {
			return fmt.Errorf("migration %d (%s): no down.sql", m.version, m.name)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.downSQL); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("down %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", m.version); err != nil {
			tx.Rollback(ctx)
			return err
		}
		return tx.Commit(ctx)
	}
	return fmt.Errorf("migration %d not found in FS", current)
}
