// covey-migcheck fails when a branch adds a migration number that main or
// another open pull request already uses under another name.
//
// Review sees one diff, so it cannot see that a second branch took the same
// number; `loadMigrations` notices only once both files sit in one tree. CI
// runs this on every pull request, before that point. The comparison itself is
// internal/db/migcheck, which the deployed-database check shares.
//
// A command of its own, and not a subcommand of `covey`: it needs nothing but
// git and the GitHub API, and `covey` embeds the web UI, so a subcommand would
// make this job wait for a frontend build that has no part in it.
//
// The branch's own additions come from git (files added under migrations/
// since the merge base with main); main and the other pull requests come from
// the GitHub API, read-only. Exit status: 0 no collision, 1 a collision, 2 the
// check could not run — an API error is never read as "nothing found".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"covey/internal/db/migcheck"
)

const (
	exitOK        = 0
	exitCollision = 1
	exitError     = 2
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("covey-migcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", getenv("GITHUB_REPOSITORY"), "owner/name of the repository whose main and open pull requests are compared")
	pr := fs.Int("pr", 0, "number of the pull request being checked, left out of the open ones (0: a local branch)")
	base := fs.String("base", "origin/main", "git ref the branch is compared against; added files are those since the merge base with it")
	head := fs.String("head", "HEAD", "git ref of the branch being checked")
	mainRef := fs.String("main", "main", "branch on GitHub whose migrations/ is read")
	dir := fs.String("dir", "migrations", "directory holding the migrations")
	api := fs.String("api", "https://api.github.com", "GitHub API base URL")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "covey-migcheck: -repo (or GITHUB_REPOSITORY) is required")
		return exitError
	}

	files, err := addedFiles(ctx, *base, *head, *dir)
	if err != nil {
		fmt.Fprintln(stderr, "covey-migcheck:", err)
		return exitError
	}
	added, err := parseUp(files)
	if err != nil {
		fmt.Fprintln(stderr, "covey-migcheck:", err)
		return exitError
	}
	// Nothing added, nothing to collide: no reason to spend API calls (the
	// unauthenticated limit is 60 an hour) or to fail on an outage.
	if len(added) == 0 {
		fmt.Fprintf(stdout, "no migration added since %s; nothing to check\n", *base)
		return exitOK
	}

	gh := &github{base: strings.TrimRight(*api, "/"), repo: *repo, token: getenv("GITHUB_TOKEN")}
	others, nPRs, err := readOthers(ctx, gh, *dir, *mainRef, *pr)
	if err != nil {
		fmt.Fprintln(stderr, "covey-migcheck:", err)
		return exitError
	}

	findings := migcheck.Compare(added, others)
	actions := getenv("GITHUB_ACTIONS") == "true"
	for _, f := range findings {
		if f.Blocking() {
			fmt.Fprintln(stdout, "collision:", f)
			if actions {
				// A workflow command, because the log of a failed step is not
				// what everyone can open; an annotation sits on the run page.
				fmt.Fprintf(stdout, "::error file=%s,title=Migration number collision::%s\n", escapeProp(f.Added.File), escapeData(f.String()))
			}
		} else {
			fmt.Fprintln(stdout, "same:", f)
		}
	}
	blocking := migcheck.Blocking(findings)
	if len(blocking) > 0 {
		fmt.Fprintf(stdout, "%d collision(s) among %d added migration(s); renumber the newer branch to the next free number\n", len(blocking), len(added))
		return exitCollision
	}
	fmt.Fprintf(stdout, "ok: %d added migration(s) checked against main and %d other open PR(s)\n", len(added), nPRs)
	return exitOK
}

// readOthers collects the other side: every migration on main, and every
// migration another open pull request adds. It returns the number of pull
// requests read.
func readOthers(ctx context.Context, gh *github, dir, mainRef string, self int) ([]migcheck.Other, int, error) {
	var others []migcheck.Other

	onMain, err := gh.listDir(ctx, dir, mainRef)
	if err != nil {
		return nil, 0, err
	}
	mainMigs, err := parseUp(onMain)
	if err != nil {
		return nil, 0, fmt.Errorf("%s on %s: %w", dir, mainRef, err)
	}
	for _, m := range mainMigs {
		others = append(others, migcheck.Other{Migration: m, Source: migcheck.Source{Kind: migcheck.SourceMain}})
	}

	prs, err := gh.openPRs(ctx)
	if err != nil {
		return nil, 0, err
	}
	n := 0
	for _, p := range prs {
		if p.Number == self {
			continue
		}
		n++
		fs, err := gh.addedFiles(ctx, p.Number)
		if err != nil {
			return nil, 0, err
		}
		var inDir []string
		for _, f := range fs {
			if path.Dir(f) == dir {
				inDir = append(inDir, f)
			}
		}
		migs, err := parseUp(inDir)
		if err != nil {
			return nil, 0, fmt.Errorf("PR #%d: %w", p.Number, err)
		}
		for _, m := range migs {
			others = append(others, migcheck.Other{Migration: m, Source: migcheck.Source{Kind: migcheck.SourcePR, PR: p.Number, Branch: p.Head.Ref}})
		}
	}
	return others, n, nil
}

// addedFiles lists the files head adds under dir since its merge base with
// base. --no-renames: a migration moved to a new number is an added number,
// not a rename git would hide behind the old name.
func addedFiles(ctx context.Context, base, head, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-renames", "--diff-filter=A", "--name-only", "-z", base+"..."+head, "--", dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("git diff %s...%s: %w: %s", base, head, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("git diff: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		// Only the directory itself: a file in a subdirectory is not a migration.
		if f != "" && path.Dir(f) == path.Clean(dir) {
			files = append(files, f)
		}
	}
	return files, nil
}

// parseUp turns paths into migrations, skipping what is not an .up.sql file.
func parseUp(files []string) ([]migcheck.Migration, error) {
	var out []migcheck.Migration
	for _, f := range files {
		m, err := migcheck.Parse(f)
		if errors.Is(err, migcheck.ErrNotUp) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// escapeData and escapeProp are the escapes GitHub's workflow commands ask for.
func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
