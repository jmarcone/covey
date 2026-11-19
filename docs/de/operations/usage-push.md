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

- `--source` benennt die Sitzungen auf der Kostenseite (1–64 Zeichen; mit
  `--source-per-project` ein optionales Präfix, siehe unten). Ein
  erneuter Push derselben Quelle ersetzt schon gesendete Aufrufe (nach
  Nachrichten-ID); ein zweiter Lauf ändert also nichts.
- `--meter-secret KEY[:SLOT]` verbindet die Quelle mit dem Abo-Zugang, dessen
  Wochenzähler sie teilt. covey teilt dann die echten Kosten jeder Stunde nach
  Listenpreis-Anteil zwischen seinen Agenten und diesen Sitzungen. Bei
  Nutzungsabrechnung weglassen.
- `--since 2026-11-18T00:00:00Z` sendet nur Aufrufe ab diesem Zeitpunkt. Weil
  ältere Aufrufe fehlen, sendet ein Push mit `--since` kein `reported_through`
  (siehe unten) und sagt das.
- `--host NAME` ist der Rechner, unter dem die Aufrufe und die Behauptung
  abgelegt werden. Standard ist der Name der Maschine, und WSL2 meldet den
  Windows-Namen; ein Push aus WSL und einer aus Windows würden also als ein
  Rechner behaupten, obwohl sie verschiedene Transkript-Ablagen lesen. Für
  **jede Transkript-Ablage ein eigenes `--host`** verwenden: `home-win` und
  `home-wsl`, ein anderer OS-Benutzer oder ein mit `CLAUDE_CONFIG_DIR`
  gesetztes Verzeichnis.
- `--complete` besagt, dass die genannten Verzeichnisse **alle**
  Claude-Code-Transkripte dieses Rechners für diese Quelle enthalten (in der
  Regel das ganze `~/.claude/projects`). Nur dann sendet der Push
  `reported_through`. Es versichert auch, dass kein Transkript dieser Ablage
  aufgeräumt wurde, bevor es gepusht war: Claude Code löscht alte Transkripte
  nach `cleanupPeriodDays` (Standard 30), und ein ungepusht gelöschter Aufruf
  fehlt der Behauptung. Den ersten Push mit `--complete` zügig machen und ihn
  dann regelmäßig laufen lassen.
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
  nicht verfolgt, ebenso wenig Junctions unter Windows; jeder wird auf stderr gemeldet), mit einer Zeile, die nicht
  gelesen werden konnte (siehe unten), oder mit zurückgehaltenen Aufrufen.
  Der Push sagt jedes Mal, warum er keine Behauptung gesendet hat, und nennt
  Datei und Zeile jeder unlesbaren Zeile und jedes zurückgehaltenen Aufrufs
  (höchstens zwanzig, nie deren Inhalt). Ohne
  `--complete` sendet er nur die Aufrufe und sagt, dass die Aufteilung des
  Zählers auf einen Push mit `--complete` wartet. Auch ein Lauf ohne neue
  Aufrufe sendet die Behauptung, also den Push auch auf ruhigen Rechnern
  planen.
- Eine Zeile mit „usage“, die sich nicht lesen lässt (kaputtes JSON, keine
  Nachrichten-ID, kein Zeitstempel), zählt als übersprungen, und die Zahl wird
  ausgegeben. Nur die letzte Zeile einer Datei, die in den letzten 30 Minuten
  geändert wurde, gilt als noch in Arbeit, und nur wenn ihr Text keinen Zeitstempel enthält, der
  älter als fünfzehn Minuten ist (eine durch einen Absturz abgeschnittene Zeile,
  an die der nächste Eintrag angehängt wurde, hat einen); jede andere übersprungene Zeile
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

## Eine Quelle je Projekt

`--source-per-project` teilt die Sitzungen nach Claude-Code-Projekt auf: Jeder
Projektordner (ein Verzeichnis direkt unter `~/.claude/projects`, eines je
Arbeitsverzeichnis, in dem Claude Code lief) ist eine eigene Quelle. covey
zählt alle Quellen zu „außerhalb von covey“ zusammen und zeigt den Anteil
jeder einzelnen.

```bash
covey usage push ~/.claude/projects --source-per-project --complete --host home-wsl \
  --label=-home-me-src-covey="covey operator" --meter-secret claude_code_oauth_token:0
```

- Das **Label** ist der Name des Ordners ohne das Home-Verzeichnis. Claude
  Code nennt den Ordner von `C:\Users\me\projects\shop`
  `C--Users-me-projects-shop` und den von `/home/me/shop` `-home-me-shop`
  (jedes Zeichen außer Buchstaben und Ziffern wird zu `-`); ihre Labels sind
  `projects-shop` und `shop`. Der Ordner des Home-Verzeichnisses selbst
  heißt `~`. Das Home wird aus jedem Ordnernamen gelesen:
  `<Laufwerk>--Users-<Name>-`, `-home-<Name>-`, `-Users-<Name>-` (macOS),
  `-root-` und `--wsl-localhost-<Distribution>-home-<Name>-` für ein
  WSL-Verzeichnis, das unter Windows geöffnet wurde.
- `--home-prefix HOME` nennt das Home-Verzeichnis stattdessen, als Pfad
  (`C:\Users\jean.marc`, `/home/me`) oder kodiert (`C--Users-jean-marc`). Es
  darf mehrfach angegeben werden; das längste, das zu einem Ordner passt,
  gilt, und die Ableitung ist dann aus. Ein Benutzername mit einem Zeichen
  außer Buchstaben und Ziffern braucht es: `jean.marc` wird zu `jean-marc`,
  und am Ordnernamen lässt sich das nicht von einem Benutzer `jean` mit einem
  Projekt `marc-…` unterscheiden.
- Ein Ordner, dessen Home nicht erkannt wird (ein Projekt außerhalb des
  Home, `D:\work\x` heißt `D--work-x`), hält den Push an, der alle solchen
  Ordner aufzählt: Sein Name könnte einen Benutzernamen enthalten.
  `--home-prefix` oder ein `--label` angeben.
- `--label ORDNER=LABEL` (mehrfach) benennt einen Ordner, zum Beispiel das
  Projekt des covey-Operators `"covey operator"`. Ein `--label` für einen
  Ordner, den es nicht gibt, ist ein Fehler; ein Tippfehler sendet so nicht
  das abgeleitete Label.
- `--source PRÄFIX` ist hier optional: Damit heißt jede Quelle
  `PRÄFIX: LABEL` (`home: covey operator`), ohne nur das Label. Jede Quelle
  samt Präfix muss 1–64 Zeichen UTF-8 ohne NUL lang sein; eine längere hält
  den Push an (dem Ordner ein kürzeres `--label` geben). Ebenso zwei Ordner,
  die dieselbe Quelle bekämen. Die Ordner `C--…` und `c--…` eines
  Windows-Verzeichnisses (Claude Code schreibt den Laufwerksbuchstaben mal
  groß, mal klein) sind ein Projekt.
- **Datenschutz:** Nur das Label und die Token-Zahlen verlassen den Rechner.
  Kein Pfad, kein Ordnername, kein Benutzername, kein Inhalt. Sitzungs-ID und
  Herkunft ergeben sich wie oben aus dem Ort jeder Datei unter ihrem
  Projektordner.
- Jede Quelle wird in eigenen Requests mit eigenem `reported_through`
  gesendet, unter demselben `--host` und `--meter-secret`. Mit `--complete`
  behauptet jede Quelle: Sie ist ein ganzer Projektordner, ganz gelesen, also
  schon dadurch vollständig. Ein Verzeichnis-Symlink, eine unlesbare Zeile
  oder ein zurückgehaltener Aufruf verhindert nur die Behauptung des Ordners,
  in dem er liegt; `--since` und ein fehlendes `--complete` verhindern jede
  Behauptung. Mit `--complete` sendet ein Projektordner ohne Transkripte
  (Claude Code hat sie aufgeräumt, nur `memory/` ist übrig) eine Behauptung
  ohne Aufrufe, damit seine letzte Behauptung die Aufteilung des Zählers
  nicht aufhält. Das ganze `~/.claude/projects` nennen, damit beide
  Schreibweisen eines Windows-Ordners gelesen werden.
- Ein Aufruf, der in zwei Projektordnern steht, wird einmal gesendet, mit dem
  Ordner, der die bessere Kopie hat.
- `--dry-run` zeigt zusätzlich eine Zeile je Quelle: Aufrufe, Tokens und ob
  sie behaupten würde. Mit `--json` stehen die Quellen unter `sources`, und
  die Hinweise gehen nach stderr, damit stdout nur das JSON enthält.
- Ein genanntes Verzeichnis, das selbst Transkripte oder Sitzungsordner
  enthält, ist ein Projekt; der Ordner einer einzelnen Sitzung wird
  abgelehnt.
- Je Rechner ab dem ersten Push festlegen. covey behält die Aufrufe einer
  Quelle; ein Rechner, der unter einem `--source` gepusht hat und dann
  wechselt, zählte seine Aufrufe in „außerhalb von covey“ doppelt.

## Alle zehn Minuten

Das ganze `~/.claude/projects` nennen und `--complete` angeben, damit der Push
behaupten darf, jeden Aufruf des Rechners gesendet zu haben. Liegen auf dem
Rechner Transkripte auch anderswo, diese Verzeichnisse ebenfalls nennen oder
`--complete` weglassen.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

Mit einer Quelle je Projekt:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --source-per-project --complete --label=-home-me-src-covey="covey operator" --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
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
