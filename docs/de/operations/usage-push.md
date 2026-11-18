---
slug: usage-push
title: Sitzungen außerhalb von covey
description: "covey usage push: die Token-Zählungen von Claude-Code-Sitzungen außerhalb von covey senden, damit die Kostenseite die ganze Delle im Abo zeigt."
---

Claude-Code-Sitzungen, die covey von außen steuern (eine Operator-Sitzung auf
dem Laptop, ein Work-Agent), laufen auf demselben Abo-Zähler wie covey's
Agenten oder auf derselben Nutzungsrechnung. `covey usage push` liest ihre
Transkripte auf dem Rechner, auf dem sie entstanden sind, und sendet **nur
Token-Zählungen** an covey: je Modellaufruf Nachrichten-ID, Sitzungs-ID,
Zeitpunkt, Modell und die fünf Token-Zahlen. Kein Prompt, keine Antwort und
keine Tool-Ausgabe verlässt den Rechner.

```bash
export COVEY_URL=http://localhost:8494
export COVEY_API_KEY=covey_…            # Schlüssel eines org_admin- oder security-Sitzes
covey usage push ~/.claude/projects/<projekt> --source "operator: laptop" \
  --meter-secret claude_code_oauth_token:0
```

- `--source` benennt die Sitzungen auf der Kostenseite (1–64 Zeichen). Ein
  erneuter Push derselben Quelle ersetzt schon gesendete Aufrufe (nach
  Nachrichten-ID); ein zweiter Lauf ändert also nichts.
- `--meter-secret KEY[:SLOT]` verbindet die Quelle mit dem Abo-Zugang, dessen
  Wochenzähler sie teilt. covey teilt dann die echten Kosten jeder Stunde nach
  Listenpreis-Anteil zwischen seinen Agenten und diesen Sitzungen. Bei
  Nutzungsabrechnung weglassen.
- `--since 2026-11-18T00:00:00Z` sendet nur Aufrufe ab diesem Zeitpunkt.
- `--dry-run` zeigt, was gesendet würde (Aufrufe und Tokens je Herkunft und
  Modell), und sendet nichts. Dafür braucht es weder URL noch Schlüssel.
- `--key-file PFAD` liest den Schlüssel aus einer Datei statt aus
  `COVEY_API_KEY`. Die Datei darf für Gruppe und andere nicht lesbar sein
  (`chmod 600`). Von der Kommandozeile wird der Schlüssel nie genommen.
- Mehrere Verzeichnisse sind möglich. Gelesen werden Hauptsitzungen,
  `subagents/` und `workflows/`; gestreamte Kopien eines Aufrufs zählen einmal.

## Alle zehn Minuten

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects/mein-projekt --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (Benutzer-Units):

```ini
# ~/.config/systemd/user/covey-usage.service
[Service]
Type=oneshot
Environment=COVEY_URL=http://localhost:8494
ExecStart=%h/bin/covey usage push %h/.claude/projects/mein-projekt --source "operator: laptop" --key-file %h/.config/covey/usage.key

# ~/.config/systemd/user/covey-usage.timer
[Timer]
OnCalendar=*:0/10
Persistent=true
[Install]
WantedBy=timers.target
```

`systemctl --user enable --now covey-usage.timer`
