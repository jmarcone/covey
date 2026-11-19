---
slug: usage-push
title: Sitzungen außerhalb von covey
description: "covey usage push: die Token-Zählungen von Claude-Code-Sitzungen außerhalb von covey senden, damit die Kostenseite die ganze Delle im Abo zeigt."
---

Claude-Code-Sitzungen, die covey von außen steuern (eine Operator-Sitzung auf
dem Laptop, ein Work-Agent), laufen auf demselben Abo-Zähler wie coveys
Agenten oder auf derselben Nutzungsrechnung. `covey usage push` liest ihre
Transkripte auf dem Rechner, auf dem sie entstanden sind, und sendet **nur
Token-Zählungen** an covey: je Modellaufruf Nachrichten-ID, Sitzungs-ID,
Zeitpunkt, Modell und die fünf Token-Zahlen. Kein Prompt, keine Antwort und
keine Tool-Ausgabe verlässt den Rechner.

```bash
export COVEY_URL=http://localhost:8494
export COVEY_API_KEY=covey_…            # Schlüssel eines org_admin- oder security-Sitzes
covey usage push ~/.claude/projects/<projekt> --source "operator: laptop" \
  --host laptop --meter-secret claude_code_oauth_token:0
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
- `--host NAME` (Pflicht) ist der Rechner, unter dem die Aufrufe und die
  Behauptung abgelegt werden. Einen Standard gibt es nicht: Der Name der
  Maschine enthält oft einen Benutzernamen (`DESKTOP-MARCO`), und WSL2
  meldet den Windows-Namen; ein Push aus WSL und einer aus Windows würden
  also als ein Rechner behaupten, obwohl sie verschiedene Transkript-Ablagen
  lesen. Für **jede Transkript-Ablage ein eigenes `--host`** verwenden:
  `home-win` und `home-wsl`, ein anderer OS-Benutzer oder ein mit
  `CLAUDE_CONFIG_DIR` gesetztes Verzeichnis.
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
  nicht verfolgt, ebenso wenig Junctions unter Windows; jeder wird auf stderr gemeldet), mit einem Eintrag, der weder Datei
  noch Verzeichnis ist (ein ins Leere zeigender Link: Wohin er zeigte,
  könnten Aufrufe liegen), mit einer Zeile, die nicht gelesen werden konnte
  (siehe unten), oder mit zurückgehaltenen Aufrufen.
  Der Push sagt jedes Mal, warum er keine Behauptung gesendet hat, und nennt
  Datei und Zeile jeder unlesbaren Zeile und jedes zurückgehaltenen Aufrufs
  (höchstens zwanzig, nie deren Inhalt). Ohne
  `--complete` sendet er nur die Aufrufe und sagt, dass die Aufteilung des
  Zählers auf einen Push mit `--complete` wartet. Auch ein Lauf ohne neue
  Aufrufe sendet die Behauptung, also den Push auch auf ruhigen Rechnern
  planen.
- Jede Zeile muss vollständiges JSON sein. Eine Zeile, die es nicht ist (von
  einem abgebrochenen Prozess abgeschnitten; ihr „usage“ fehlt dann zuerst,
  weil der Inhalt davor steht), und eine Zeile mit „usage“, die keinen
  Aufruf ergibt (keine Nachrichten-ID, kein Zeitstempel), zählt als
  übersprungen, und die Zahl wird ausgegeben. Nur die letzte Zeile einer
  Datei, die in den letzten fünfzehn Minuten geändert wurde, also nach dem
  Zeitpunkt, für den behauptet wird, gilt als noch in Arbeit, und nur wenn
  ihr Text keinen Zeitstempel enthält, der älter als fünfzehn Minuten ist
  (eine durch einen Absturz abgeschnittene Zeile, an die der nächste Eintrag
  angehängt wurde, hat einen); die Behauptung bleibt dann unter dem
  Änderungszeitpunkt dieser Datei. Jede andere übersprungene Zeile
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
  Verzeichnis. Eine Sitzung wird nur mit einer UUID benannt, wie Claude Code
  Sitzungsordner und Haupttranskripte benennt: der Ordner über `subagents/`
  (das genannte Verzeichnis, wenn es ein Sitzungsordner ist) oder der
  Dateiname eines Haupttranskripts. Alles andere (der Name eines
  Projektordners, eine Agent-Datei tiefer als
  `subagents/workflows/<Lauf>/`) bekommt die Sitzung `unknown`. Ein allein
  genannter Ordner `subagents/` oder `workflows/` enthält Subagent- bzw.
  Workflow-Aufrufe, mit der Sitzung `unknown` (der Ordner darüber wird nicht
  angesehen).
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

- **`--meter-secret` oder `--no-meter` ist Pflicht.** covey verknüpft jede
  Quelle einzeln mit ihrem Zähler, durch einen Push, der den Zähler nennt.
  Jedes neue Projekt ist eine neue Quelle, und eine ohne Verknüpfung
  gepushte bliebe unverknüpft: Ihr Verbrauch würde coveys Agenten
  angerechnet. Bei jedem Lauf `--meter-secret` angeben, oder `--no-meter`
  bei Nutzungsabrechnung.
- Das **Label** ist der Name des Ordners ohne das Home-Verzeichnis. Claude
  Code nennt den Ordner von `C:\Users\me\projects\shop`
  `C--Users-me-projects-shop` und den von `/home/me/shop` `-home-me-shop`
  (jedes Zeichen außer Buchstaben und Ziffern wird zu `-`); ihre Labels sind
  `projects-shop` und `shop`. Der Ordner des Home-Verzeichnisses selbst
  heißt `~`.
- **Das Home-Verzeichnis.** Wegen dieser Kodierung sagt ein Ordnername
  nicht, wo ein Benutzername endet: `jean-marc`, `jean.marc` und
  `jean marc` werden alle zu `jean-marc`. Ein Label wird daher nur aus einem
  sicher bekannten Home abgeleitet: dem Verzeichnis über dem genannten
  `.claude/projects` (aus WSL gesehen ist `/mnt/c/Users/jean.marc` auch so
  bekannt, wie Windows es schreibt, `C--Users-jean-marc`) und dem
  Home-Verzeichnis des Benutzers, der den Push ausführt; ein unter Windows
  geöffneter WSL-Ordner (`--wsl-localhost-<Distribution>-home-<Name>-…`)
  wird in seiner `-home-…`-Form gelesen. Ein Ordner muss in genau einem
  möglichen Home liegen: Auch die Home-Verzeichnisse der anderen Benutzer
  neben einem bekannten (die übrigen Einträge von `C:\Users`,
  `/mnt/<Laufwerk>/Users`, `/home` und `/Users`) zählen dazu; ein Ordner von
  `marco-rossi` neben einem bekannten `marco` passt also zu zweien und wird
  abgelehnt. Ein Verzeichnis, das die Home-Verzeichnisse der Benutzer
  enthält oder darüber liegt (ein Laufwerk, `C:\Users`, `/home`, `/mnt/c`),
  ist nie ein bekanntes Home. Außerhalb der bekannten Home-Verzeichnisse
  wird nichts geraten: Nur ein Home selbst (`C--Users-me` ist `~`) und
  Ordner unter `/root` bekommen ein Label; jeder andere Ordner hält den Push
  an und verlangt `--home-prefix` oder `--label`.
- `--home-prefix HOME` nennt das Home-Verzeichnis stattdessen, als Pfad
  (`C:\Users\jean.marc`, `/home/me`) oder kodiert (`C--Users-jean-marc`). Es
  darf mehrfach angegeben werden; das längste, das zu einem Ordner passt,
  gilt, und nur die angegebenen werden entfernt. Ein Ordner, der auch zu
  einem längeren Home eines anderen Benutzers daneben passt, wird
  abgelehnt. Nötig, wenn die Transkripte
  nicht im `.claude/projects` des Benutzers liegen, der den Push ausführt
  (eine Kopie, `CLAUDE_CONFIG_DIR`, die Ablage eines anderen Benutzers).
- `--label ORDNER=LABEL` (mehrfach) benennt einen Ordner, zum Beispiel das
  Projekt des covey-Operators `"covey operator"`. Ein `--label` für einen
  Ordner, den es nicht gibt, ist ein Fehler; ein Tippfehler sendet so nicht
  das abgeleitete Label.
- `--source PRÄFIX` ist hier optional: Damit heißt jede Quelle
  `PRÄFIX: LABEL` (`home: covey operator`), ohne nur das Label.
- **Ein einzelner Ordner hält alle Quellen an.** Ein Ordner, dessen Home
  nicht sicher bekannt ist (ein Projekt außerhalb des Home, `D:\work\x`
  heißt `D--work-x`; die Ablage eines anderen Benutzers) oder zu zwei
  Home-Verzeichnissen passt, eine Quelle über 64 Zeichen oder zwei
  Ordner mit derselben Quelle (zwei Schreibweisen, die nicht dasselbe
  Verzeichnis sind) halten den ganzen Push an: Bis dieser Ordner ein
  `--label` bekommt (oder das Home ein `--home-prefix`), wird nichts
  gesendet und keine Quelle behauptet. Der Fehler zählt jeden solchen Ordner
  auf. Nach dem Einrichten eines Rechners und nach dem ersten Arbeiten in
  einem neuen Verzeichnis außerhalb des Home mit `--dry-run` prüfen. Die
  Ordner `C--…` und `c--…` eines Windows-Verzeichnisses (Claude Code
  schreibt den Laufwerksbuchstaben mal groß, mal klein) sind ein Projekt,
  keine Kollision.
- Ein Transkript oder Sitzungsordner ganz oben in `~/.claude/projects`,
  neben den Projektordnern, wird mit seinem Namen abgelehnt: in seinen
  Projektordner verschieben oder entfernen. Ein genanntes Verzeichnis, das
  selbst Transkripte oder Sitzungsordner und keine Projektordner enthält,
  ist ein Projekt; der Ordner einer einzelnen Sitzung wird abgelehnt.
- **Datenschutz:** Ein Request enthält die Quelle (das Label, mit dem
  `--source`-Präfix) und je Aufruf den `--host`, die Sitzungs-ID (eine UUID
  oder `unknown`, siehe oben), die Nachrichten-ID, das Modell, den
  Zeitpunkt, die Herkunft und die fünf Token-Zahlen. Kein Pfad, kein
  Ordnername, kein Benutzername, kein Inhalt: Ein Label wird nur
  abgeleitet, wenn das Home im Ordnernamen sicher bekannt ist, sonst hält
  der Push an. Ein `--label` wird so gesendet, wie es angegeben ist; ein
  `--home-prefix` wird entfernt, nicht gesendet, und was im Ordnernamen
  darauf folgt, geht als Label hinaus. `--host` wird so gesendet, wie es
  angegeben ist.
- Jede Quelle wird in eigenen Requests mit eigenem `reported_through`
  gesendet, unter demselben `--host` und `--meter-secret`. Mit `--complete`
  behauptet jede Quelle: Sie ist ein ganzer Projektordner, ganz gelesen, also
  schon dadurch vollständig. Ein Verzeichnis-Symlink, ein ins Leere
  zeigender Link, eine unlesbare Zeile, ein Verzeichnis oder eine Datei, die
  sich nicht lesen lässt, oder ein
  zurückgehaltener Aufruf verhindert nur die Behauptung des Ordners, in dem
  er liegt; `--since` und ein fehlendes `--complete` verhindern jede
  Behauptung. Ein Ordner mit Transkripten, aber ohne Aufruf darin sendet
  seine Behauptung trotzdem (einen Request ohne Aufrufe). Das ganze
  `~/.claude/projects` nennen, damit beide Schreibweisen eines
  Windows-Ordners gelesen werden.
- Ein Projektordner wird nur übersprungen (keine Quelle, kein Label nötig,
  nichts gesendet), wenn er sich ganz lesen ließ und keine `*.jsonl`-Datei
  und keinen Link enthält: Nur `memory/` liegt darin. Ein Ordner, der sich
  nicht lesen lässt oder dessen Sitzungsordner ein Symlink oder eine
  Junction ist, bleibt eine Quelle, deren Behauptung zurückgehalten wird.
- **Aufräumen:** Claude Code löscht Transkripte nach `cleanupPeriodDays`
  (Standard 30). Sind alle Transkripte eines Projekts weg, wird sein Ordner
  übersprungen, und seine Quelle behauptet nicht mehr. covey zählt den
  Rechner dieser Quelle bis zu sieben Tage nach ihrem letzten Push als
  aktiv; die Zählerstände dieses Zeitraums warten deshalb als „nicht
  gemeldet“, bis die Woche vorbei ist. Ein höheres `cleanupPeriodDays` macht
  das seltener.
- Ein Aufruf, der in zwei Projektordnern steht (eine Sitzung, die in einem
  anderen Verzeichnis fortgesetzt oder abgezweigt wurde), wird einmal
  gesendet: die Kopie mit der meisten Ausgabe, dann die früheste, dann die
  des Ordners, dessen Name zuerst sortiert; das Label spielt keine Rolle,
  ein neues Label verschiebt also keinen Aufruf. Wird die Kopie dieses
  Ordners aufgeräumt, während der andere Ordner noch eine hat, wird der
  Aufruf unter der anderen Quelle erneut gesendet, und covey zählt ihn dann
  unter beiden.
- `--dry-run` zeigt zusätzlich eine Zeile je Quelle: Aufrufe, Tokens und ob
  sie behaupten würde. Mit `--json` stehen die Quellen unter `sources`, und
  die Hinweise gehen nach stderr, damit stdout nur das JSON enthält.
- **Wechseln oder ein Label ändern.** covey behält die Aufrufe einer
  Quelle. Ein Rechner, der unter einem `--source` gepusht hat und zu
  `--source-per-project` wechselt, oder ein Ordner, dessen Label sich
  ändert, sendet seine Aufrufe unter der neuen Quelle erneut, und sie zählen
  in „außerhalb von covey“ doppelt. Die alte Quelle mit
  `DELETE /api/v1/cost/external-usage?source=…` entfernen (ihre Aufrufe,
  ihre Zähler-Verknüpfung und ihre Behauptungen); bis dahin hält sie auch die
  Aufteilung des Zählers bis zu sieben Tage nach ihrem letzten Push als
  „nicht gemeldet“ auf.

## Alle zehn Minuten

Das ganze `~/.claude/projects` nennen und `--complete` angeben, damit der Push
behaupten darf, jeden Aufruf des Rechners gesendet zu haben. Liegen auf dem
Rechner Transkripte auch anderswo, diese Verzeichnisse ebenfalls nennen oder
`--complete` weglassen.

cron:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --complete --source "operator: laptop" --host laptop --meter-secret claude_code_oauth_token:0 --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

Mit einer Quelle je Projekt:

```cron
*/10 * * * * COVEY_URL=http://localhost:8494 covey usage push $HOME/.claude/projects --source-per-project --complete --host laptop --label=-home-me-src-covey="covey operator" --meter-secret claude_code_oauth_token:0 --key-file $HOME/.config/covey/usage.key >>$HOME/.cache/covey-usage.log 2>&1
```

systemd (Benutzer-Units):

```ini
# ~/.config/systemd/user/covey-usage.service
[Service]
Type=oneshot
Environment=COVEY_URL=http://localhost:8494
ExecStart=%h/bin/covey usage push %h/.claude/projects --complete --source "operator: laptop" --host laptop --meter-secret claude_code_oauth_token:0 --key-file %h/.config/covey/usage.key

# ~/.config/systemd/user/covey-usage.timer
[Timer]
OnCalendar=*:0/10
Persistent=true
[Install]
WantedBy=timers.target
```

`systemctl --user enable --now covey-usage.timer`
