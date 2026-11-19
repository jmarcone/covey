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
- `--since 2026-11-18T00:00:00Z` sendet nur Aufrufe ab diesem Zeitpunkt. Weil
  ältere Aufrufe fehlen, sendet ein Push mit `--since` kein `reported_through`
  (siehe unten) und sagt das.
- `--complete` besagt, dass die genannten Verzeichnisse **alle**
  Claude-Code-Transkripte dieses Rechners für diese Quelle enthalten (in der
  Regel das ganze `~/.claude/projects`). Nur dann sendet der Push
  `reported_through`.
- `--dry-run` zeigt, was gesendet würde (Aufrufe und Tokens je Herkunft und
  Modell), und sendet nichts. Dafür braucht es weder URL noch Schlüssel.
- `--key-file PFAD` liest den Schlüssel aus einer Datei statt aus
  `COVEY_API_KEY`. Die Datei darf für Gruppe und andere nicht lesbar sein
  (`chmod 600`); unter Windows wird die ACL der Datei nicht geprüft, also
  gehört sie ins eigene Profil. Von der Kommandozeile wird der Schlüssel nie
  genommen.
- `reported_through` ist eine Behauptung: Jeder Aufruf dieses Rechners für
  diese Quelle bis zu diesem Zeitpunkt steckt im Push. covey teilt eine Stunde
  des Abo-Zählers zwischen seinen Agenten und diesen Sitzungen auf, sobald
  jeder Rechner der Quelle darüber hinaus behauptet hat, und schreibt auf
  covey, was die Quelle nicht gesendet hat; eine zu großzügige Behauptung
  kostet die Quelle also ihren Anteil. Eine zu frühe verzögert die Aufteilung
  nur; bis dahin gilt die Stunde als „nicht gemeldet“. Der Push sendet sie
  (Zeitpunkt des Laufs minus fünfzehn Minuten) deshalb nur mit `--complete`
  und nie, wenn Aufrufe fehlen könnten: mit `--since`, mit einem
  Verzeichnis-Symlink unterhalb eines genannten Verzeichnisses (Symlinks werden
  nicht verfolgt; jeder wird auf stderr gemeldet), mit einer Zeile, die nicht
  gelesen werden konnte (siehe unten), oder mit zurückgehaltenen Aufrufen.
  Der Push sagt jedes Mal, warum er keine Behauptung gesendet hat. Ohne
  `--complete` sendet er nur die Aufrufe und sagt, dass die Aufteilung des
  Zählers auf einen Push mit `--complete` wartet. Auch ein Lauf ohne neue
  Aufrufe sendet die Behauptung, also den Push auch auf ruhigen Rechnern
  planen.
- Eine Zeile mit „usage“, die sich nicht lesen lässt (kaputtes JSON, keine
  Nachrichten-ID, kein Zeitstempel), zählt als übersprungen, und die Zahl wird
  ausgegeben. Nur die letzte Zeile einer Datei, die in den letzten 30 Minuten
  geändert wurde, gilt als noch in Arbeit; jede andere übersprungene Zeile
  verhindert die Behauptung. Aufrufe mit einer Zahl über einer Milliarde oder
  unter null oder mit einem Text über 256 Bytes lehnt covey ab; der Push hält
  sie zurück und listet sie auf. Ein leeres Modell wird als `unknown`
  gesendet.
- Ein genanntes Verzeichnis ohne jede `*.jsonl`-Datei ist ein Fehler, auch wenn
  andere Verzeichnisse welche haben. `--host` muss UTF-8 ohne NUL sein. Eine
  einfache `http://`-URL zu einem anderen Rechner löst eine Warnung aus:
  Schlüssel und Zahlen würden unverschlüsselt übertragen.
- Ein Verzeichnis kann ein Projekt (`~/.claude/projects/<projekt>`), ganz
  `~/.claude/projects` oder der Ordner einer Sitzung sein: Sitzung und
  Herkunft ergeben sich aus dem Ort jeder Datei, nicht aus dem genannten
  Verzeichnis.
- Mehrere Verzeichnisse sind möglich. Gelesen werden Hauptsitzungen,
  `subagents/` und `workflows/`; gestreamte Kopien eines Aufrufs zählen einmal.

## Alle zehn Minuten

Das ganze `~/.claude/projects` nennen und `--complete` angeben, damit der Push
behaupten darf, jeden Aufruf des Rechners gesendet zu haben. Liegen auf dem
Rechner Transkripte auch anderswo, diese Verzeichnisse ebenfalls nennen oder
`--complete` weglassen.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (Benutzer-Units):

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
