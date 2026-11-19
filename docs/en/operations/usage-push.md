---
slug: usage-push
title: Sessions outside covey
description: "covey usage push: sending the token counts of Claude Code sessions that run outside covey, so the cost page shows the whole dent in the subscription."
---

Claude Code sessions that drive covey from outside (an operator session on a
laptop, a work agent) run on the same subscription meter as covey's agents,
or on the same per-use bill. `covey usage push` reads their transcripts on the
machine they were written on and sends **only token counts** to covey: per
model call the message id, session id, timestamp, model and the five token
counts. No prompt, answer or tool output leaves the machine.

```bash
export COVEY_URL=http://localhost:8494
export COVEY_API_KEY=covey_…            # a key of an org_admin or security seat
covey usage push ~/.claude/projects/<project> --source "operator: laptop" \
  --meter-secret claude_code_oauth_token:0
```

- `--source` names the sessions on the cost page (1–64 characters; with
  `--source-per-project` an optional prefix, see below). Every
  push of the same source replaces calls it already sent (by message id), so
  running it again changes nothing.
- `--meter-secret KEY[:SLOT]` links the source to the subscription credential
  whose weekly meter it shares. covey then splits each hour's real cost
  between its agents and these sessions by their list-price share. Leave it
  out for a per-use account.
- `--since 2026-11-18T00:00:00Z` sends only calls from then on. Because older
  calls are left out, a `--since` push sends no `reported_through` (see below)
  and says so.
- `--host NAME` is the host the calls and the claim are filed under. It
  defaults to the machine's name, and WSL2 reports the Windows name, so a push
  from WSL and one from Windows would claim as one host although they read
  different transcript stores. Use **one distinct `--host` per transcript
  store**: `home-win` and `home-wsl`, another OS user, or a directory set with
  `CLAUDE_CONFIG_DIR`.
- `--complete` says that the directories named hold **every** Claude Code
  transcript of this host for this source (normally the whole
  `~/.claude/projects`). Only then does the push send `reported_through`.
  It also asserts that no transcript of that store was cleaned up before it
  was pushed: Claude Code deletes old transcripts after `cleanupPeriodDays`
  (default 30), and a call deleted unpushed is lost to the claim. Do the first
  `--complete` push promptly, then keep it scheduled.
- `--dry-run` prints what would be sent (calls and tokens per origin and per
  model) and sends nothing. It needs neither URL nor key.
- `--key-file PATH` reads the key from a file instead of `COVEY_API_KEY`. The
  file must not be readable by group or others (`chmod 600`); on Windows the
  file's ACL is not checked, so keep it in your own profile. The key is never
  taken from the command line.
- `reported_through` is a claim: every call of this host for this source up
  to that time is in the push. covey splits an hour of the subscription meter
  between its agents and these sessions once every host of the source has
  claimed past it, and books what the source did not send to covey, so a
  claim that is too generous costs the source its share. A claim that is too
  early only delays the split; until then the hour shows as "not reported".
  The push therefore sends it (the time of the run, less fifteen minutes)
  only with `--complete`, and never when calls may be missing: with
  `--since`, with a symlinked directory below a named one (they are not
  followed, and neither are junctions on Windows; each is warned about on
  stderr), with a line that could not be
  read (see below), or with calls held back. Each time the push says why no
  claim was sent, and lists the file and line of every unreadable line and
  held-back call (twenty at most; never their contents). Without `--complete` it sends the calls only and says that
  the meter split waits until a `--complete` push. A run with no new calls
  still sends the claim, so schedule the push even on quiet machines.
- A line with a usage that cannot be read (broken JSON, no message id, no
  timestamp) is counted as skipped, and the count is printed. Only the last
  line of a file changed in the last 30 minutes is taken for a reply still
  being written, and only if its text holds no timestamp older than fifteen
  minutes (a line cut off by a crash and followed by the next record has
  one); any other skipped line sends no claim. Calls with a count
  above one billion or below zero, or with a text over 256 bytes, are
  refused by covey; the push holds them back and lists them. An empty model
  is sent as `unknown`.
- A named directory without any `*.jsonl` file is an error, even when other
  directories have some. `--host` must be UTF-8 without NUL. A plain `http://`
  URL to another machine draws a warning: the key and the counts would travel
  unencrypted.
- A directory may be one project (`~/.claude/projects/<project>`), all of
  `~/.claude/projects`, or one session's folder: the session and its origin
  come from each file's own place, not from the directory named.
- Several directories may be named at once. Main sessions, `subagents/` and
  `workflows/` transcripts are read; streamed copies of one call count once.

## One source per project

`--source-per-project` splits the sessions by Claude Code project: each
project folder (a top-level directory under `~/.claude/projects`, one per
working directory Claude Code ran in) is a source of its own. covey adds all
sources up to "outside covey" and shows each one's share.

```bash
covey usage push ~/.claude/projects --source-per-project --complete --host home-wsl \
  --label=-home-me-src-covey="covey operator" --meter-secret claude_code_oauth_token:0
```

- **`--meter-secret` or `--no-meter` is required.** covey links a source to
  its meter per source, from a push that names the meter. Every new project
  is a new source, and one pushed without the link would stay unlinked: its
  use would be booked to covey's agents. Give `--meter-secret` on every run,
  or `--no-meter` for a per-use account.
- The **label** is the folder's name without the home directory. Claude Code
  names the folder of `C:\Users\me\projects\shop` `C--Users-me-projects-shop`
  and that of `/home/me/shop` `-home-me-shop` (every character other than a
  letter or digit becomes `-`); their labels are `projects-shop` and `shop`.
  The home directory's own folder is `~`.
- **The home directory.** Because of that encoding a folder name does not
  say where a user name ends: `jean-marc`, `jean.marc` and `jean marc` are
  all `jean-marc`. So the push first strips the homes it knows for sure: the
  directory above the `.claude/projects` named (seen from WSL,
  `/mnt/c/Users/jean.marc` is also known as Windows writes it,
  `C--Users-jean-marc`), and the home directory of the user running the
  push. Only for folders outside those does it read the home from the name:
  `<drive>--Users-<name>-`, `-home-<name>-`, `-Users-<name>-` (macOS),
  `-root-`, and `--wsl-localhost-<distro>-home-<name>-` for a WSL directory
  opened from Windows; the name is then a known user name when one fits,
  else one run of letters and digits. If that run is only the start of a
  known user name (`jean` of `jean-marc`), the push stops rather than send
  the rest of the name as a label.
- `--home-prefix HOME` names the home directory instead, as a path
  (`C:\Users\jean.marc`, `/home/me`) or encoded (`C--Users-jean-marc`). It
  may be given several times; the longest that fits a folder wins, and only
  the homes given are stripped. Give it when the transcripts are not in a
  `.claude/projects` of their user (a copy, `CLAUDE_CONFIG_DIR`) and the user
  name holds a character other than a letter or digit.
- `--label FOLDER=LABEL` (several times) labels one folder, for example the
  covey operator's project `"covey operator"`. A `--label` for a folder that
  is not there is an error, so a typo does not send the derived label.
- `--source PREFIX` is optional here: with it each source is
  `PREFIX: LABEL` (`home: covey operator`), without it the label alone.
- **One stray folder stops every source.** A folder whose home is not
  recognised (a project outside the home, `D:\work\x` is `D--work-x`) or is
  ambiguous, a source over 64 characters, or two folders with one source
  (two spellings that are not the same directory) stop the whole push: until
  that folder gets a `--label` (or the home a `--home-prefix`), nothing is
  sent and no source claims. The error lists every such folder. Run with
  `--dry-run` after setting up a machine and after starting work in a new
  directory outside the home. The `C--…` and `c--…` folders of one Windows
  directory (Claude Code writes the drive letter either way) are one
  project, not a collision.
- A transcript or session folder lying at the top of `~/.claude/projects`,
  beside the project folders, is refused with its name: move it into its
  project folder or away. A named directory that holds transcripts or
  session folders itself, and no project folders, is one project; a
  session's own folder is refused.
- **Privacy:** only the label and the token counts leave the machine. No
  path, no folder name, no user name, no content. Session id and origin are
  read as above, from each file's place below its project folder; a
  `subagents/` directly in a project folder, with no session folder above
  it, gets the session `unknown`, never the folder's name.
- Each source is sent in its own requests with its own `reported_through`,
  under the same `--host` and `--meter-secret`. With `--complete` each source
  claims: it is one whole project folder, read whole, so it is complete by
  construction. A symlinked directory, an unreadable line, a directory or
  file that cannot be read, or a held-back call withholds only the claim of
  the folder it is in; `--since` and a missing `--complete` withhold every
  claim. A folder with transcripts but no call in them still sends its claim
  (a request without calls). Name the whole `~/.claude/projects`, so that
  both spellings of a Windows folder are read.
- A project folder is skipped (no source, no label needed, nothing sent)
  only when it could be read whole and holds no `*.jsonl` file and no link:
  only `memory/` is there. A folder that cannot be read, or whose session
  folder is a symlink or junction, stays a source with its claim withheld.
- **Cleanup:** Claude Code deletes transcripts after `cleanupPeriodDays`
  (default 30). Once all of a project's transcripts are gone, its folder is
  skipped and its source stops claiming. covey counts the host of that
  source as active for up to seven days after its last push, so the meter
  readings of that window wait as "not reported" until the week has passed.
  Raising `cleanupPeriodDays` makes this rarer.
- A call found in two project folders (a session resumed or forked into
  another directory) is sent once: the copy with the most output, then the
  earliest, then the folder whose name sorts first; the label plays no part,
  so relabelling moves no call. When that folder's copy is cleaned up while
  the other folder still holds one, the call is sent again under the other
  source, and covey then counts it under both.
- `--dry-run` adds one line per source: calls, tokens, and whether it would
  claim. With `--json` the sources are under `sources`, and the notes go to
  stderr so that stdout holds the JSON only.
- **Switching, or changing a label.** covey keeps a source's calls. A
  machine that pushed under one `--source` and switches to
  `--source-per-project`, or a folder whose label changes, sends its calls
  again under the new source, and they count twice in "outside covey".
  Remove the old source with `DELETE /api/v1/cost/external-usage?source=…`
  (its calls, meter link and claims); until then it also holds the meter
  split back, as "not reported", for up to seven days after its last push.

## After an upgrade

Since 748a2a19, on equal output the later streamed copy of a call is kept
(as the measurement script does), where the earlier one was before. The
first push after the upgrade therefore reports a number of calls as
updated, once; later pushes report them unchanged.

## Every ten minutes

Name the whole `~/.claude/projects` and pass `--complete`, so the push may
claim to have sent every call of the host. If the machine keeps transcripts
elsewhere too, name those directories as well, or leave `--complete` out.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --meter-secret claude_code_oauth_token:0 --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

With one source per project:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --source-per-project --complete --label=-home-me-src-covey="covey operator" --meter-secret claude_code_oauth_token:0 --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (user units):

```ini
# ~/.config/systemd/user/covey-usage.service
[Service]
Type=oneshot
Environment=COVEY_URL=http://localhost:8494
ExecStart=%h/bin/covey usage push %h/.claude/projects --complete --source "operator: laptop" --meter-secret claude_code_oauth_token:0 --key-file %h/.config/covey/usage.key

# ~/.config/systemd/user/covey-usage.timer
[Timer]
OnCalendar=*:0/10
Persistent=true
[Install]
WantedBy=timers.target
```

`systemctl --user enable --now covey-usage.timer`
