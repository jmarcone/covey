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

- The **label** is the folder's name without the home directory. Claude Code
  names the folder of `C:\Users\me\projects\shop` `C--Users-me-projects-shop`
  and that of `/home/me/shop` `-home-me-shop` (every character other than a
  letter or digit becomes `-`); their labels are `projects-shop` and `shop`.
  The home directory's own folder is `~`. The home is read from each folder
  name: `<drive>--Users-<name>-`, `-home-<name>-`, `-Users-<name>-` (macOS),
  `-root-`, and `--wsl-localhost-<distro>-home-<name>-` for a WSL directory
  opened from Windows.
- `--home-prefix HOME` names the home directory instead, as a path
  (`C:\Users\jean.marc`, `/home/me`) or encoded (`C--Users-jean-marc`). It
  may be given several times; the longest that fits a folder wins, and the
  derivation is then off. A user name with a character other than a letter
  or digit needs it: `jean.marc` is encoded `jean-marc`, which the folder
  name cannot tell from a user `jean` with a project `marc-…`.
- A folder whose home is not recognised (a project outside the home,
  `D:\work\x` is `D--work-x`) stops the push, which lists every such folder:
  its name might hold a user name. Give `--home-prefix` or a `--label`.
- `--label FOLDER=LABEL` (several times) labels one folder, for example the
  covey operator's project `"covey operator"`. A `--label` for a folder that
  is not there is an error, so a typo does not send the derived label.
- `--source PREFIX` is optional here: with it each source is
  `PREFIX: LABEL` (`home: covey operator`), without it the label alone. Each
  source, prefix included, must be 1–64 characters of UTF-8 without NUL; a
  longer one stops the push (give that folder a shorter `--label`). So do two
  folders that would get the same source. The `C--…` and `c--…` folders of
  one Windows directory (Claude Code writes the drive letter either way) are
  one project.
- **Privacy:** only the label and the token counts leave the machine. No
  path, no folder name, no user name, no content. Session id and origin are
  read as above, from each file's place below its project folder.
- Each source is sent in its own requests with its own `reported_through`,
  under the same `--host` and `--meter-secret`. With `--complete` each source
  claims: it is one whole project folder, read whole, so it is complete by
  construction. A symlinked directory, an unreadable line or a held-back call
  withholds only the claim of the folder it is in; `--since` and a missing
  `--complete` withhold every claim. With `--complete` a project folder
  without transcripts (Claude Code cleaned them up, only `memory/` is left)
  sends a claim without calls, so that its last claim does not hold the meter
  split back. Name the whole `~/.claude/projects`, so that both spellings of
  a Windows folder are read.
- A call found in two project folders is sent once, with the folder holding
  its better copy.
- `--dry-run` adds one line per source: calls, tokens, and whether it would
  claim. With `--json` the sources are under `sources`, and the notes go to
  stderr so that stdout holds the JSON only.
- A named directory that holds transcripts or session folders itself is one
  project; a session's own folder is refused.
- Choose per machine from its first push. covey keeps a source's calls, so a
  machine that pushed under one `--source` and then switches would count its
  calls twice in "outside covey".

## Every ten minutes

Name the whole `~/.claude/projects` and pass `--complete`, so the push may
claim to have sent every call of the host. If the machine keeps transcripts
elsewhere too, name those directories as well, or leave `--complete` out.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

With one source per project:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --source-per-project --complete --label=-home-me-src-covey="covey operator" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (user units):

```ini
# ~/.config/systemd/user/covey-usage.service
[Service]
Type=oneshot
Environment=COVEY_URL=http://localhost:8494
ExecStart=%h/bin/covey usage push %h/.claude/projects --complete --source "operator: laptop" --key-file %h/.config/covey/usage.key

# ~/.config/systemd/user/covey-usage.timer
[Timer]
OnCalendar=*:0/10
Persistent=true
[Install]
WantedBy=timers.target
```

`systemctl --user enable --now covey-usage.timer`
