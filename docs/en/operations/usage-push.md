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
- `--since 2026-11-18T00:00:00Z` sends only calls from then on.
- `--dry-run` prints what would be sent (calls and tokens per origin and per
  model) and sends nothing. It needs neither URL nor key.
- `--key-file PATH` reads the key from a file instead of `COVEY_API_KEY`. The
  file must not be readable by group or others (`chmod 600`). The key is never
  taken from the command line.
- Several directories may be named at once. Main sessions, `subagents/` and
  `workflows/` transcripts are read; streamed copies of one call count once.

## Every ten minutes

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects/my-project --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (user units):

```ini
# ~/.config/systemd/user/covey-usage.service
[Service]
Type=oneshot
Environment=COVEY_URL=http://localhost:8494
ExecStart=%h/bin/covey usage push %h/.claude/projects/my-project --source "operator: laptop" --key-file %h/.config/covey/usage.key

# ~/.config/systemd/user/covey-usage.timer
[Timer]
OnCalendar=*:0/10
Persistent=true
[Install]
WantedBy=timers.target
```

`systemctl --user enable --now covey-usage.timer`
