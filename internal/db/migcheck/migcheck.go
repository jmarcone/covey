// Package migcheck compares migration version numbers across places that
// cannot see each other: a branch, main, the other open pull requests, a
// deployed database.
//
// Two branches hand out their numbers against the same main, and each one is
// fine on its own. Review sees one diff, `loadMigrations` (migrate.go) rejects
// a shared number only once both files sit in one tree, and `MigrateUp` refuses
// a version, or a name, that a database applied under another one (migrate.go,
// `unapplied`) — but only at start-up, so a collision shows up after review.
// This package is the comparison that closes that gap before the merge.
//
// It does no I/O and has no database: callers read the files, the pull
// requests or the schema_migrations rows and hand over plain values. The CI
// command (cmd/covey-migcheck) feeds it files; the check against a deployed
// database feeds it applied rows. Keeping the source of the other side generic
// is what lets both share this one comparison.
package migcheck

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// upSuffix marks the file that carries a migration; the .down.sql beside it
// has the same version and name and tells us nothing more.
const upSuffix = ".up.sql"

// ErrNotUp is what Parse returns for a file that is not an NNNN_name.up.sql.
// A caller walking a directory listing skips these (errors.Is) and still sees
// a file that claims to be a migration but is malformed.
var ErrNotUp = errors.New("not an .up.sql file")

// Migration is one migration as the other places know it.
type Migration struct {
	Version int
	// Name is everything before ".up.sql", number included
	// ("0142_stage_keys") — the same string `schema_migrations.name` holds,
	// so a row read from the database compares with a file read from a branch.
	Name string
	// File is the path the migration was read from, when it came from a file;
	// empty for an applied row. Only used to word a finding.
	File string
}

// Parse reads NNNN_<name>.up.sql, with or without a directory in front.
func Parse(file string) (Migration, error) {
	base := path.Base(file)
	name, ok := strings.CutSuffix(base, upSuffix)
	if !ok {
		return Migration{}, fmt.Errorf("%s: %w", file, ErrNotUp)
	}
	digits, rest, ok := strings.Cut(name, "_")
	if !ok || rest == "" {
		return Migration{}, fmt.Errorf("migration %s: expected NNNN_name", file)
	}
	// Atoi alone would let "+142" and "-1" through.
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return Migration{}, fmt.Errorf("migration %s: version %q is not a number", file, digits)
	}
	version, err := strconv.Atoi(digits)
	if err != nil {
		return Migration{}, fmt.Errorf("migration %s: version: %w", file, err)
	}
	return Migration{Version: version, Name: name, File: file}, nil
}

// SourceKind says where the other side of a comparison came from.
type SourceKind string

const (
	// SourceMain is a migration file on main.
	SourceMain SourceKind = "main"
	// SourcePR is a migration another open pull request adds.
	SourcePR SourceKind = "pr"
	// SourceApplied is a row of schema_migrations on a deployed database.
	SourceApplied SourceKind = "applied"
)

// Source names the other side. Only the fields of its Kind are read.
type Source struct {
	Kind SourceKind

	// SourcePR
	PR     int
	Branch string

	// SourceApplied
	AppliedAt time.Time
}

// String words the source for a message: "main", "PR #2 (fix/stage-keys)",
// "the deployed database (applied 2026-11-17T09:30:00Z)".
func (s Source) String() string {
	switch s.Kind {
	case SourceMain:
		return "main"
	case SourcePR:
		return fmt.Sprintf("PR #%d (%s)", s.PR, s.Branch)
	case SourceApplied:
		if s.AppliedAt.IsZero() {
			return "the deployed database"
		}
		return "the deployed database (applied " + s.AppliedAt.UTC().Format(time.RFC3339) + ")"
	}
	return string(s.Kind)
}

// Other is one migration on the other side, with where it came from.
type Other struct {
	Migration
	Source Source
}

// FindingKind tells a collision from a harmless repeat.
type FindingKind string

const (
	// Collision: the same version under another name. Whichever merges
	// second is the one that breaks.
	Collision FindingKind = "collision"
	// SameName: the same version under the same name — a stacked pull request
	// carries its base's migration, or the file has already landed. Not a
	// problem; reported so the output shows the match was seen.
	SameName FindingKind = "same version and same name"
)

// Finding is one match between an added migration and one on the other side.
type Finding struct {
	Kind  FindingKind
	Added Migration
	Other Other
}

// Blocking reports whether the finding should fail a check.
func (f Finding) Blocking() bool { return f.Kind == Collision }

// String words the finding in one line, naming both sides.
func (f Finding) String() string {
	if f.Kind == SameName {
		return fmt.Sprintf("%s carries version %04d under the same name as %s in %s — not a collision",
			label(f.Added), f.Added.Version, label(f.Other.Migration), f.Other.Source)
	}
	return fmt.Sprintf("%s collides with %s in %s — both take version %04d under different names",
		label(f.Added), label(f.Other.Migration), f.Other.Source, f.Added.Version)
}

func label(m Migration) string {
	if m.File != "" {
		return m.File
	}
	return m.Name
}

// Compare matches every added migration against the other side by version.
// Findings come in the order of added, then of others. No match, no finding.
func Compare(added []Migration, others []Other) []Finding {
	byVersion := make(map[int][]Other, len(others))
	for _, o := range others {
		byVersion[o.Version] = append(byVersion[o.Version], o)
	}
	var out []Finding
	for _, a := range added {
		for _, o := range byVersion[a.Version] {
			kind := Collision
			if a.Name == o.Name {
				kind = SameName
			}
			out = append(out, Finding{Kind: kind, Added: a, Other: o})
		}
	}
	return out
}

// Blocking returns the findings that should fail a check.
func Blocking(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Blocking() {
			out = append(out, f)
		}
	}
	return out
}
