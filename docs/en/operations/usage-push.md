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

- `--source` names the sessions on the cost page (1–64 characters). Every
  push of the same source replaces calls it already sent (by message id), so
  running it again changes nothing.
- `--meter-secret KEY[:SLOT]` links the source to the subscription credential
  whose weekly meter it shares. covey then splits each hour's real cost
  between its agents and these sessions by their list-price share. Leave it
  out for a per-use account.
- `--since 2026-11-18T00:00:00Z` sends only calls from then on. Because older
  calls are left out, a `--since` push sends no `reported_through` (see below)
  and says so.
- `--complete` says that the directories named hold **every** Claude Code
  transcript of this host for this source (normally the whole
  `~/.claude/projects`). Only then does the push send `reported_through`.
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
  followed; each is warned about on stderr), with a line that could not be
  read (see below), or with calls held back. Each time the push says why no
  claim was sent. Without `--complete` it sends the calls only and says that
  the meter split waits until a `--complete` push. A run with no new calls
  still sends the claim, so schedule the push even on quiet machines.
- A line with a usage that cannot be read (broken JSON, no message id, no
  timestamp) is counted as skipped, and the count is printed. Only the last
  line of a file changed in the last 30 minutes is taken for a reply still
  being written; any other skipped line sends no claim. Calls with a count
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

## Every ten minutes

Name the whole `~/.claude/projects` and pass `--complete`, so the push may
claim to have sent every call of the host. If the machine keeps transcripts
elsewhere too, name those directories as well, or leave `--complete` out.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
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
