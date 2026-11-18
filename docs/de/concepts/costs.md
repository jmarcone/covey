---
slug: kosten
title: Kosten & Token
description: 'Was die vier Zahlen der Kostenseite bedeuten und wann es sie gibt, wie die echten Kosten eines Abonnements aus dem Zähler des Anbieters geschätzt werden, wie die Rechnung eines API-Schlüssels gelesen und abgeglichen wird und was die Seite noch nicht zählt.'
faq:
  - q: Warum zeigt die Seite für dieselbe Arbeit mehrere Dollarbeträge?
    a: 'Sie beantworten verschiedene Fragen. „Von Claude Code gemeldet“ ist der eigene Preis der Engine für ihre Zeilen, „Aus Tokens nachgerechnet“ sind dieselben Tokens mit der Tabelle von covey bepreist, „Echte Kosten (Schätzung)“ ist, was ein Abo-Platz von seinem Wochenkontingent verbraucht hat, und „Von Anthropic berechnet“ ist, was ein API-Schlüssel gekostet hat. Sie stehen je Zugangsdaten nebeneinander und werden nie über Zugangsdaten hinweg addiert.'
  - q: Warum zeigt eine Stunde 0 echte Kosten, obwohl Agenten gearbeitet haben?
    a: 'Die echten Kosten kommen aus dem Zähler des Anbieters, und covey liest den Zähler nur, wenn ein Agent auf diesen Zugangsdaten aufwacht oder solange ein Lauf Daten streamt. Ein Balken zeigt die Punkte, die in dieser Stunde seit der vorigen Ablesung abgelesen wurden; eine Stunde ohne Ablesung zeigt 0, und Arbeit aus einer früheren Stunde kann in einer späteren erscheinen. Über einen längeren Zeitraum addiert sich die Bewegung auf, auf einen Punkt je Woche genau.'
  - q: Warum gibt es für eine einzelne Stunde oder für einen Agenten kein „Listen-$ je echtem $“?
    a: 'Der Zähler bewegt sich in ganzen Prozent, wenige Punkte sind also nur auf einen Punkt genau bekannt, und er gilt für das ganze Konto, nicht für einen Agenten. Das Vielfache erscheint ab 3 Punkten Bewegung, über einen Zeitraum, für ganze Zugangsdaten.'
  - q: Wo hinterlege ich, was das Abonnement kostet?
    a: 'Auf der Kostenseite im Block der Zugangsdaten: „Tarifpreis setzen“. Das dürfen Organisationsadmins und die Sicherheitsrolle. Der Preis bewertet die Zählerpunkte, die ab „Gültig ab“ abgelesen wurden, nie davor.'
  - q: Zählt die Seite meine eigenen Claude-Code-Sitzungen außerhalb von covey?
    a: 'Noch nicht. In jeder Zahl stecken nur Läufe der Agenten von covey. Sitzungen, die covey von außen steuern, bewegen den Zähler eines gemeinsamen Abonnements trotzdem.'
---

Die Kostenseite (*Kosten* in der Seitenleiste) zeigt, was die Arbeit der Agenten
verbraucht hat. Jede Rolle darf sie lesen; einen Tarifpreis setzen dürfen
Organisationsadmins und die Sicherheitsrolle. Alle Zeiträume werden in UTC
geschnitten: UTC-Tage, Wochen, die montags UTC beginnen.

Die Seite trägt bis zu vier Dollarzahlen für dieselbe Arbeit. Sie stammen aus
vier Quellen, werden getrennt gehalten, und die Seite addiert nie eine zur
anderen — und auch nicht die Zahlen zweier Zugangsdaten. *Zugangsdaten* sind der
geheime Wert, mit dem ein Lauf hinausging (ein API-Schlüssel oder ein
Abo-Token); ihre Art sagt, was Geld dort bedeutet.

## Die vier Zahlen

| Zahl | Was sie ist | Es gibt sie, wenn | Es gibt sie nicht, wenn |
|---|---|---|---|
| **Von Claude Code gemeldet** | Der eigene Preis von Claude Code für seine Zeilen: die Summe der `costUSD`, die es auf jede Ergebniszeile schreibt, jeweils der eigene Anteil einer Zeile. Keine Buchung von covey. | Der Lauf geht über die Engine Claude Code und seine Ergebniszeile trägt die Nutzung je Modell. | Die Engine schreibt keine solche Zeile (Codex meldet Tokens und kein Geld, siehe [Runtimes](runtimes.md)). Ein Lauf, der vor seiner Ergebniszeile abgebrochen wurde, steht weiter unten. |
| **Aus Tokens nachgerechnet** | Die Tokens derselben Zeilen, je Art bepreist — Eingabe, Ausgabe, Cache-Lesen, Cache-Schreiben für 5 Minuten und für 1 Stunde, Websuchen — mit der eingebauten Preistabelle von covey. | Das Modell steht in der Tabelle. | Die Tabelle kennt das Modell nicht: Seine Zeilen zählen dann mit dem eigenen Betrag von Claude Code, ohne Aufteilung nach Art, und sind als *Unbekanntes Modell (Preis der Engine)* markiert. |
| **Echte Kosten (Schätzung)** | Was der Wochenzähler eines Abo-Platzes sich bewegt hat, mal der Preis je Punkt des Tarifs. | Die Zugangsdaten sind ein Abonnement, ihr Zähler wurde gelesen und ein Tarifpreis ist hinterlegt. | Die Zugangsdaten sind ein API-Schlüssel; noch kein Agent ist auf ihnen aufgewacht (*keine Zählerablesung für diese Zugangsdaten*); kein Tarifpreis (*kein Tarifpreis hinterlegt*); oder der Zähler hat sich keinen ganzen Punkt bewegt. |
| **Von Anthropic berechnet** | Was die Usage-and-Cost-API von Anthropic für den API-Schlüssel hinter den Zugangsdaten meldet, nach Rabatten und vor Gutschriften. | Die Zugangsdaten sind ein API-Schlüssel, ein Anthropic-Admin-Schlüssel ist hinterlegt, die Zugangsdaten sind ihrem API-Schlüssel zugeordnet und die Rechnung für den Zeitraum ist eingetroffen. | Eines davon fehlt. Die Seite sagt dann welches — *kein Anthropic-Admin-Schlüssel hinterlegt*, *keinem Anthropic-API-Schlüssel zugeordnet*, *der Abgleich der Rechnung schlug fehl*, *Anthropics Daten sind noch nicht da* — und zeigt keine Zahl. Für eine Rechnung, die nicht gelesen werden konnte, steht nie 0,00 $. |

Aus der Herkunft der ersten beiden folgt:

- **„Gemeldet“ und „nachgerechnet“ kommen aus denselben Zeilen.** Die zweite
  prüft die Preistabelle, nicht die Tokens: Eine Lücke dazwischen heißt, dass ein
  Preis abweicht. Die *Preistabellen-Prüfung* daneben ist diese Lücke in Prozent.
- **Ein Lauf, der ohne Ergebniszeile stirbt, hat trotzdem Geld gekostet.** Claude
  Code führt die laufende Summe einer Sitzung. Zeigt eine spätere Zeile, dass die
  Summe um mehr gewachsen ist, als ihr eigener Anteil und die Zeilen dazwischen
  erklären, legt covey die Differenz als eigene Zeile an und bucht sie als
  eigenen Kosteneintrag. Sie wird nur hinzugefügt, nie abgezogen, und nur, wo
  beide Enden bekannt sind: Die erste Zeile einer Sitzung legt keine an.

Eine Buchung, mit der keine Zeile verknüpft ist (ein Lauf, der vor seinem
Ergebnis abgebrochen wurde, eine Engine ohne Ergebniszeilen, eine Buchung, die
älter ist als die vorhandenen Aufzeichnungen), hat keine Tokens zum Messen. Sie
steckt nicht in den Zahlen; die Seite nennt ihren Betrag, und im gestapelten
Diagramm ist sie der Teil *Nicht aufgeschlüsselt*. Buchungen, die ein älterer
Daemon aus laufenden Summen gemacht hat (Basis `legacy` oder `cumulative`),
stecken ebenfalls nicht in den Zahlen; die Seite zählt sie unter
*Buchungsprüfung*.

## Wie die echten Kosten geschätzt werden

Ein Claude-Code-Abonnement hat einen Zähler: den bisher verbrauchten Anteil eines
Wochenkontingents, in Prozent. covey liest ihn, nimmt, wie weit er sich bewegt
hat, und bewertet die Bewegung.

**Zählerablesungen.** Es gibt zwei Quellen, die auf der Seite unterschieden
werden:

- *`/usage`* — covey fragt Claude Code (`claude -p /usage`) einmal je
  Zugangsdaten, wenn ein Agent darauf aufwacht, und hält die Antwort fünf
  Minuten, sodass eine Flotte auf einem Platz einmal fragt. Die Antwort ist Prosa
  in ganzen Prozent. Begrenzt der Anbieter die Frage, gibt die Engine eine ältere
  Antwort zurück; sie ist als veraltet markiert und wird nicht benutzt.
- *Im Strom des Laufs* — das eigene `rate_limit_event` von Claude Code, das jedes
  Fenster nennt, das der Anbieter meldet, mit Auslastung und Rücksetzzeit. Es
  kostet nichts, ist aber nur da, solange ein Lauf läuft.

Beide lesen den Zähler des *Kontos*. Alles andere, was das Konto nutzt — eine
Person in claude.ai, eine andere Maschine —, bewegt ihn mit. Ablesungen werden je
geheimem Wert aufbewahrt, ein Umsortieren der Zugangsdaten bricht die Geschichte
also nicht ab. Haben beide Quellen dieselben Zugangsdaten gelesen, nimmt die
Seite die mit den meisten Ablesungen (bei Gleichstand `/usage`) und addiert nie
beide: Sie lesen denselben Zähler.

Bepreist wird nur das Wochenfenster über alle Modelle. Die übrigen Fenster
(fünf Stunden, ein Wochenlimit für ein einzelnes Modell) werden gezeigt und nie
bepreist, weil sie dieselbe Arbeit noch einmal zählen würden.

**Was ein Punkt wert ist.** Der Preis eines Tarifs wird auf die Wochen seines
Zeitraums verteilt — ein Monat sind 52/12 Wochen, ein Jahr 52 —, und ein Punkt
des Wochenzählers ist ein Hundertstel davon. Bei 200 $ im Monat:

```
200 × 12 / 52 = 46,15 $ je Woche  →  0,46 $ je Punkt
```

Jede Ablesung wird mit dem Tarif bepreist, der galt, als sie gelesen wurde; eine
Preisänderung gilt also ab ihrem Beginn und nicht rückwirkend. Ein Punkt kostet
nur dann so viel, wenn das Wochenkontingent aufgebraucht wird; bei der Hälfte des
Kontingents ist ein verbrauchter Punkt doppelt so viel wert. Deshalb ist die Zahl
eine Schätzung.

**Die Bewegung.** Innerhalb einer Woche steigt der Zähler nur, eine Ablesung, die
hinterherhinkt, trägt also nichts bei. Die erste Ablesung, die die Rechnung für
Zugangsdaten sieht, ist deren Ausgangspunkt und zählt 0; gezählt wird ab der
letzten Ablesung vor dem Zeitraum (bis zu acht Tage zurück), oder, wo es keine
gibt, ab der ersten darin — dann steckt nicht in der Zahl, was sich davor
bewegt hat. Veraltete und nicht unterstützte Ablesungen bleiben draußen.

**Das Wochen-Zurücksetzen.** Eine Ablesung beginnt eine neue Woche, wenn die
Rücksetzzeit, die sie nennt, nach vorn gerückt ist; nennt sie keine, wenn das
Zurücksetzen der vorigen Ablesung verstrichen ist; und nur wo keine Rücksetzzeit
etwas sagt, wenn der Zähler um mehr als einen Punkt gefallen ist. Die erste
Ablesung einer neuen Woche zählt voll — der Zähler fing bei 0 an —, und eine
Ablesung, die zu einer bereits vergangenen Woche gehört, wird verworfen. Ein
Zeitraum, der ein Zurücksetzen überspannt, ist deshalb eine Summe von Wochen.

**Ganze Prozent, also eine Spanne.** Der Zähler zeigt ganze Prozent: *n* Punkte
Bewegung liegen irgendwo zwischen *n* − 1 und *n* + 1, und jede Woche im
Zeitraum lässt ihren eigenen Punkt Unschärfe. Die Kachel zeigt die Schätzung mit
ihrer Spanne, zum Beispiel 12 Punkte zu 0,46 $ sind 5,54 $, Spanne 5,08 $ bis
6,00 $.

**Stündliche Auflösung.** Bei stündlichen Zeiträumen zeigt ein Balken die Punkte,
die in dieser Stunde seit der vorigen Ablesung abgelesen wurden. covey liest den
Zähler, wenn ein Agent auf den Zugangsdaten aufwacht oder solange ein Lauf
streamt, also gilt:

- eine Stunde ohne Ablesung hat keine Bewegung und zeigt **0** — das heißt
  „nicht gelesen“, nicht „nichts verbraucht“;
- Arbeit aus früheren Stunden erscheint in der Stunde der nächsten Ablesung, nicht
  in der Stunde, in der sie getan wurde;
- eine Stunde sind nur wenige Punkte, jeder davon ±1.

**Das Vielfache von Liste gegen echt.** *Listen-$ je echtem $* teilt den
nachgerechneten Listenpreis der Zeilen der Zugangsdaten zwischen dem Ausgangspunkt
des Zählers und seiner letzten Ablesung durch die echten Kosten desselben
Zeitraums. Nur über einen Zeitraum hat es Aussagekraft:

- Es erscheint erst ab **3 Punkten** Bewegung, mit seiner Spanne. Darunter sagt
  die Seite, wie wenige Punkte sich der Zähler bewegt hat, und zeigt nur die
  Spanne.
- Eine einzelne Stunde erreicht selten 3 Punkte, und ihre Punkte sind nicht die
  Arbeit der Stunde (siehe oben). Ein Zeitraum im Diagramm bekommt ein Vielfaches
  nach der Regel der Kachel — ab 3 Punkten, mindestens ein Cent Listenpreis —, und
  seine Spanne steht in den Details des Zeitraums.
- Es ist keine Schranke, in keiner Richtung. Andere Nutzung des Kontos bewegt den
  Zähler mit, wodurch das wahre Vielfache höher liegt; ein nicht ausgeschöpftes
  Kontingent macht jeden Punkt teurer als sein Preis hier, wodurch es niedriger
  liegt.
- Es braucht die ganzen Zugangsdaten: Mit Agentenfilter oder in der Ansicht *Je
  Agent* gibt es kein Vielfaches, weil der Zähler keine Agenten kennt. Die Tabelle
  zeigt stattdessen den *≈ Anteil an den echten Kosten (Schätzung)* je gewähltem Agenten: seinen Anteil am
  nachgerechneten Listenpreis, angewandt auf die echten Kosten der Zugangsdaten.
- Jeder Punkt braucht einen Tarifpreis. Fehlt er für einige, sagt die Seite *kein
  Tarifpreis für N von M Punkten* und zeigt weder echte Kosten noch ein
  Vielfaches.

## Die Rechnung und der Abgleich mit ihr

Berechnetes Geld wird gelesen, nie gebucht: Die Kosteneinträge von covey bleiben,
was der Daemon gemeldet hat, und die Rechnung wird daneben gespeichert.

**Einrichten.** Drei Schritte; nur Organisationsadmins und die Sicherheitsrolle
dürfen sie tun.

1. Einen Anthropic-Admin-API-Schlüssel (`sk-ant-admin…`) oder einen
   `read:analytics`-Schlüssel unter **Secrets** als `anthropic_admin_key`
   ablegen. Der Schlüssel gehört allein der Organisation; er erreicht nie einen
   Agenten und kein Endpunkt gibt ihn zurück, und covey lehnt einen Wert
   `sk-ant-admin…` unter jedem anderen Namen ab. Beim Speichern prüft covey den
   Schlüssel gegen die Analytics-API von Anthropic und sagt es, wenn hier aus
   Versehen ein API-Schlüssel oder ein Abo-Token abgelegt wurde.
2. Sagen, welcher API-Schlüssel alle nach Verbrauch abgerechneten Zugangsdaten sind,
   mit `PATCH /api/v1/runtime-instances/{id}/credentials/{ord}/billing-key` und
   `{"api_key_id": "apikey_01…"}` (optional `"workspace_id": "wrkspc_01…"`).
   Dafür gibt es noch keine Oberfläche. `GET /api/v1/cost/billing-keys` listet die
   Schlüssel, die die Rechnung der letzten 30 Tage nennt, mit ihren Beträgen, zum
   Aussuchen. Die Zuordnung folgt dem geheimen Wert, ab dem Zeitpunkt, an dem er
   geschrieben wurde; ersetzt man den Wert, endet sie. Ein Abo-Platz hat keinen
   API-Schlüssel und wird abgelehnt, und ein API-Schlüssel gehört immer nur zu
   einem Zugang.
3. Warten. covey liest die Rechnung einmal pro Stunde — die letzten zwei Tage in
   Stundenabschnitten und einmal am Tag 35 Tage in Tagesabschnitten, was
   nachholt, was Anthropic korrigiert hat. Das erste Lesen kommt wenige Minuten
   nach dem Start des Servers. `POST /api/v1/cost/billing-sync` liest sofort
   (`?full=1` fügt das Nachlesen der 35 Tage hinzu). Ein Lesen, das scheitert,
   behält die Zeilen des letzten, das gelang, und sagt, warum.

**Was die Seite zeigt.** Die Rechnung kommt 4 bis 24 Stunden verspätet und ist
daher nur bis zu einem Zeitpunkt vollständig, den die Seite nennt
(*vollständig bis …, der Rest ausstehend*). Die Kachel summiert die Rechnung über
ganze Rechnungsabschnitte — Stunden innerhalb der letzten zwei Tage, davor
UTC-Tage — und stellt die gemeldeten und nachgerechneten Zahlen genau dieses
Zeitraums daneben, dort abgeschnitten, wo die Rechnung nicht mehr vollständig ist.
Eine Rechnung über einen Zeitraum wird nie mit Zeilen über einen anderen
verglichen. *Berechnet / Liste* ist die Rechnung nach Rabatten geteilt durch den
Listenpreis, den Anthropic für dieselbe Nutzung nennt.

**Der Abgleich.** `GET /api/v1/cost/reconcile?days=<n>` vergleicht je UTC-Tag,
zugeordneten Zugangsdaten und Modell, was Claude Code gemeldet hat, was die
Tabelle von covey aus denselben Tokens macht (*Liste*) und was Anthropic für den
Schlüssel berechnet hat — mit Token-Zahlen je Art auf beiden Seiten (`diff` ist
berechnet minus gemeldet). Der *Rest* (`residual`) ist der Listenpreis von
Anthropic minus der von covey: das, was Anthropic bepreist hat und die Tokens von
covey nicht erklären. Sein Etikett sagt, wie man ihn liest:

| `residual_label` | Bedeutung |
|---|---|
| `match` | Weniger als ein Cent auseinander. |
| `billed_above_list` | Anthropic hat mehr bepreist, als die Tokens von covey erklären: andere Nutzung desselben Schlüssels außerhalb von covey, Tokens, die den Zeilen entgangen sind, oder ein Preis, den die Tabelle nicht kennt. |
| `billed_below_list` | Die Tokens von covey ergeben einen höheren Preis als die Liste von Anthropic für den Schlüssel. Die Token-Differenzen sagen, welche Art abweicht. |
| `incomplete` | Der Tag ist in den bei Anthropic eingetroffenen Daten noch nicht zu Ende; seine Zahlen können noch wachsen. |
| `not_comparable` | Eine Zeile oder eine berechnete Zeile lief außerhalb von Standard-Tier, Standard-Geschwindigkeit oder globaler Geografie, die Listenpreise nicht abdecken. |
| `no_billed_data` | Der Tag ist vollständig und die Rechnung hat keine Zeile für diese Zugangsdaten und dieses Modell. |

Ein *Rabatt* (`discount`) ist der Listenpreis von Anthropic minus sein Betrag und
damit keine Lücke. `runs_without_result` zählt Läufe, die an dem Tag ohne jede
Ergebniszeile gebucht wurden: Ihr Verbrauch steckt in der Rechnung und nirgends
auf der Seite von covey. Ein Modell, das die Tabelle nicht kennt, ist als
*unpriced* markiert, und die Liste von covey deckt dann nur ab, was sie kennt.

## Filter

- **Zeitraum und Abschnitte.** 24 h, 7, 30 oder 90 Tage, mit Abschnitten von einer
  Stunde, einem Tag, einer Woche oder einem Monat. Die Wahl eines Zeitraums wählt
  einen Abschnitt (24 h stündlich, 90 Tage wöchentlich). Stunden gibt es nur bis
  zu 7 Tagen: 90 Tage stündlich wären 2.160 Zeiträume.
- **Agenten.** Chips, in jeder Kombination; *Alle* heißt, dass niemand
  ausgeschlossen ist. Ein Klick fügt einen hinzu oder nimmt ihn heraus, ein
  Doppelklick wählt nur diesen. Die Agenten sind nach Abteilung gruppiert: Die
  Abteilungen sind nach ihren Kosten im Zeitraum geordnet, die Agenten ohne
  Abteilung stehen zuletzt unter *Keine Abteilung*, und die Überschrift einer
  Abteilung wählt die ganze Gruppe aus oder ab. Eine Gruppe zeigt ihre ersten
  fünf Chips und klappt den Rest unter *+N weitere* ein; ein gewählter Chip wird
  nie versteckt. Die Abteilung ist die *jetzige* des Agenten, nicht die zur Zeit
  der Buchung — covey führt keine Geschichte der Wechsel. Hat niemand eine
  Abteilung, ist der Filter eine flache Zeile.
- **Zugangsdaten.** Kein Chip: Das Diagramm hat einen Block je Zugangsdaten mit
  eigenen Kacheln und einen Block für Läufe, die keinen Zugangsdaten zugeordnet
  werden konnten. Über die API lässt sich die Tabelle je Modell mit `?secret_key=`
  auf `GET /api/v1/cost/models` auf einen Zugang eingrenzen.
- **Quelle.** Vier Chips, einer je Zahl. *Von Claude Code gemeldet* und *Aus
  Tokens nachgerechnet* sind standardmäßig an, *Echte Kosten* und *Von Anthropic
  berechnet* aus. Echte Kosten und die Rechnung gehören zu ganzen Zugangsdaten,
  nicht zu einem Agenten: Mit Agentenfilter erscheinen sie nur als geschätzter
  Anteil in der Tabelle.
- **Diagramm.** Aufteilen nach Kostenart oder gar nicht; die zu zeichnenden
  Kostenarten; kombiniert oder *Je Agent*; Beträge oder *Echt gegen Liste*, das
  nur für Abo-Zugangsdaten mit Ablesungen gezeichnet wird, bei eingeschalteten
  echten Kosten, ohne Agentenfilter und mit einem Tarifpreis für jeden Punkt.

## Tarifpreis

Im Block von Abo-Zugangsdaten öffnet *Tarifpreis setzen* ein Formular mit dem
Betrag in US-Dollar, dem Zeitraum (pro Monat oder pro Jahr) und *Gültig ab*. Nur
Organisationsadmins und die Sicherheitsrolle sehen es als bearbeitbar.

- *Gültig ab* beginnt am Anfang der laufenden Zählerwoche (das letzte
  Wochen-Zurücksetzen, das die Ablesungen nennen, minus sieben Tage). Beginnt in
  dieser Woche schon ein Preis, korrigiert das Formular ihn und beginnt jetzt,
  auf die Minute.
- Ein Preis wird hinzugefügt, nie überschrieben. Der bei *Gültig ab* geltende
  Tarif endet dort; einer, der später beginnt, begrenzt den neuen. Ein zweiter
  Preis zum selben Zeitpunkt wird abgelehnt, und das Formular sagt, man solle
  einen späteren Beginn wählen.
- Er bewertet von da an die Punkte des Zählers und nur die: Nichts richtet sich
  nach ihm, und er ist kein Budget.
- Die API (`PUT /api/v1/runtime-instances/{id}/credentials/{ord}/plan`) nimmt
  auch eine Währung mit ihrem Kurs zu US-Dollar; das Formular kennt nur
  US-Dollar. Ein Tarif in anderer Währung ohne Kurs bewertet nichts.
- Der Preis folgt dem geheimen Wert, das Formular spricht aber den Platz an, an
  dem die Zugangsdaten jetzt sitzen; wurden sie umsortiert, während es offen war,
  sagt das Formular es.

## Noch nicht abgedeckt

- **Liste gegen echt gibt es je Zugangsdaten.** Es gibt keine echten Kosten oder
  kein Vielfaches für die ganze Organisation: Zwei Zugangsdaten können Plätze
  eines Kontos sein, und zwei Quellen lesen denselben Zähler, also wird nichts
  über Zugangsdaten oder Quellen hinweg addiert.
- **Sitzungen eines Operators außerhalb von covey.** Claude-Code-Sitzungen, die
  covey von außen steuern — die eines Operators —, stecken in keiner Zahl, bei
  keiner Quelle. Auf einem gemeinsamen Abonnement bewegen sie den Zähler des
  Kontos trotzdem, die gezeigten echten Kosten sind also das, was das ganze Konto
  verbraucht hat, nicht das, was die Agenten von covey getan haben. Verfolgt als
  Issue #120.
- **Die Zuordnung von Schlüsseln zu Zugangsdaten und der Abgleich haben keine
  Oberfläche.** Beides gibt es nur über die API; die Seite zeigt das Ergebnis der
  Rechnung in der Kachel *Von Anthropic berechnet*.
- **Ablesungen nur für Claude Code.** Andere Engines haben keine Zählerablesung,
  also keine echten Kosten.

## Weiter

- [Runtimes und Engines](runtimes.md) — Zugangsdaten, Plätze und was jede Engine meldet
- [Identität & Secrets](identity-and-secrets.md) — wo Zugangsdaten oder der Admin-Schlüssel abgelegt werden
- [Guard-Rails](guard-rails.md) — das Budget je Agent, eine andere Zahl als diese
