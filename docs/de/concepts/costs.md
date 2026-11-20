---
slug: kosten
title: Kosten & Token
description: 'Womit die Kostenseite führt und wie man sie liest: die ganze Arbeit zu Tokenpreisen, die eine echte Kostenzahl des Abonnements, das eine Vielfache und die Claude-Code-Sitzungen außerhalb von covey, die standardmäßig mitzählen. Wie die echten Kosten aus dem Zähler des Anbieters geschätzt werden, wie die Rechnung eines API-Schlüssels gelesen und abgeglichen wird und was die Seite noch nicht zählt.'
faq:
  - q: Welche Zahl führt die Seite, und warum gibt es weitere Dollarbeträge?
    a: 'Eine Listenpreis-Zahl führt: die ganze Arbeit des Zeitraums zu Tokenpreisen, wie von Claude Code gemeldet. Die anderen beantworten andere Fragen. „Echte Kosten (Schätzung)“ ist, was ein Abo-Platz von seinem Wochenkontingent verbraucht hat, „Von Anthropic berechnet“ ist, was ein API-Schlüssel gekostet hat, und coveys eigene Preistabelle erscheint als Differenzzeile unter der führenden Zahl, als eigene Zahl in Kacheln und Diagramm hinter „Mit coveys Preistabelle vergleichen“ und als Spalte „Nachgerechnet“ der Tabelle je Modell, die standardmäßig an ist. Die Blöcke der Zugangsdaten addieren nie über Zugangsdaten hinweg; die Kopfzeile addiert die Abo-Zähler, und die Tabelle je Modell hat Zeilen, die alle Abo-Zugänge und alle API-Schlüssel summieren (siehe *Filter*).'
  - q: Zählt die Seite meine eigenen Claude-Code-Sitzungen außerhalb von covey?
    a: 'Ja, sobald sie mit covey usage push gesendet wurden. Sie sind standardmäßig in jeder Zahl enthalten, immer als außerhalb von covey gekennzeichnet, und der Chip „nur covey“ nimmt sie aus Kacheln, Diagramm und Tabellen heraus. Die Kopfzeile zeigt weiter den ganzen Verbrauch. Siehe *Sitzungen außerhalb von covey*.'
  - q: Was bedeutet das Vielfache?
    a: 'Die ganze Arbeit auf den Abo-Zählern zu Tokenpreisen (die Agenten von covey und die Sitzungen außerhalb von covey, addiert), geteilt durch die eine echte Kostenzahl dieser Zähler im selben Zeitraum. Die Kopfzeile nennt außerdem eine Untergrenze: das Vielfache am unteren Ende der Spanne, die die ganzen Prozente des Zählers lassen. Wo ein Zähler Nutzung enthalten kann, die niemand meldet, ist das Vielfache eine Untergrenze, keine Schätzung.'
  - q: Warum zeigt eine Stunde 0 echte Kosten, obwohl Agenten gearbeitet haben?
    a: 'Die echten Kosten kommen aus dem Zähler des Anbieters, und covey liest den Zähler nur, wenn ein Agent auf diesen Zugangsdaten aufwacht oder solange ein Lauf Daten streamt. Ein Balken zeigt die Punkte, die in dieser Stunde seit der vorigen Ablesung abgelesen wurden; eine Stunde ohne Ablesung zeigt 0, und Arbeit einer früheren Stunde kann in einer späteren erscheinen. Über einen längeren Zeitraum addiert sich die Bewegung, auf einen Punkt je Woche genau.'
  - q: Warum gibt es für eine einzelne Stunde oder für einen Agenten kein „Listen-$ je echtem $“?
    a: 'Der Zähler bewegt sich in ganzen Prozent, wenige Punkte sind also nur auf einen Punkt genau bekannt, und er gilt für das ganze Konto, nicht für einen Agenten. Das Vielfache erscheint ab 3 Punkten Bewegung, über einen Zeitraum, für ganze Zugangsdaten.'
  - q: Wo hinterlege ich, was das Abonnement kostet?
    a: 'Auf der Kostenseite im Block der Zugangsdaten: „Tarifpreis setzen“, und „Kostengrundlage festlegen“ für das, was auf diesen Zugangsdaten das Geld ist (der Platz, seine Nutzung oder beides). Beides dürfen Organisationsadmins und die Sicherheitsrolle. Ein Tarifpreis bewertet die Zählerpunkte, die ab „Gültig ab“ abgelesen wurden, nie davor.'
---

Die Kostenseite (*Kosten* in der Spalte *Verwaltung* der Seitenleiste; die Seite heißt *Kosten & Token*) zeigt, was die Arbeit der Agenten
verbraucht hat, und was Claude-Code-Sitzungen außerhalb von covey daneben
verbraucht haben. Jede Rolle darf sie lesen; einen Tarifpreis oder eine
Kostengrundlage setzen dürfen Organisationsadmins und die Sicherheitsrolle. Alle
Zeiträume werden in UTC geschnitten: UTC-Tage, Wochen, die montags UTC beginnen.

*Zugangsdaten* sind der geheime Wert, mit dem ein Lauf hinausging (ein
API-Schlüssel oder ein Abo-Token); ihre Art sagt, was Geld dort bedeutet. Die
Seite hält ihre Dollarzahlen getrennt und sagt, welche Frage jede beantwortet.

## Womit die Seite führt

Drei Zahlen tragen die Seite. Alles andere ist Detail darunter.

**1. Die ganze Arbeit zu Tokenpreisen.** Beschriftet *Zu Tokenpreisen (wie von
Claude Code gemeldet)*: die Arbeit des ganzen Zeitraums, aus jeder Quelle, je
Aufruf bepreist.

- Für die Agenten von covey ist es der eigene Preis von Claude Code für jeden
  Aufruf: die `costUSD`, die es auf jede Ergebniszeile schreibt, jeweils der
  eigene Anteil einer Zeile. Keine Buchung von covey. (Codex meldet Tokens und
  kein Geld, siehe [Runtimes](runtimes.md).)
- Für Sitzungen außerhalb von covey ist es dieselbe Zahl, wo der Push sie
  mitgeschickt hat.
- Nur ein Aufruf ohne gemeldete Kosten wird aus seinen Tokens mit der
  eingebauten Preistabelle von covey nachgerechnet (Eingabe, Ausgabe,
  Cache-Lesen, Cache-Schreiben für 5 Minuten und für 1 Stunde, Websuchen). Die
  Zahl sagt es: *nachgerechnet für Quellen ohne gemeldete Kosten*. Ein Aufruf
  eines Modells, das die Tabelle nicht kennt, hat keinen Listenpreis: Seine Tokens
  zählen als unbepreist, nie als 0 $.
- coveys eigene Preistabelle ist nur eine Prüfung. Weicht sie um mehr als 1 % und
  mehr als 0,50 $ von der führenden Zahl ab, sagt es eine Zeile unter der Zahl
  (*coveys eigene Preistabelle ergibt … % weniger/mehr, als Claude Code gemeldet
  hat*). Die Tabelle je Modell ist die Ausnahme: Sie hat eine eigene Spalte
  *Nachgerechnet*, standardmäßig an (siehe *Filter*). *Mit coveys Preistabelle vergleichen* (auch `?prices=recomputed`) zeigt
  *Von Claude Code gemeldet* und *Aus Tokens nachgerechnet* als eigene Zahlen, in
  den Kacheln, im Diagramm und in seinen Tabellen. Eine Lücke dazwischen heißt,
  dass ein Preis abweicht, nicht die Tokens.

**2. Eine echte Kostenzahl.** Was die Wochenbewegung der Abo-Zähler gekostet hat:
die Punkte, die sich der Zähler bewegt hat, mal der Preis je Punkt des Tarifs. Es
gibt sie nur für ein Abonnement mit Zählerablesungen und einem Tarifpreis (siehe
*Wie die echten Kosten gelesen werden*). Sie ist eine Schätzung (*≈*), und der
Zähler gilt für das ganze Konto: Was sonst das Konto nutzt, bewegt ihn auch.

**3. Ein Vielfaches.** Die Arbeit zu Tokenpreisen (die Agenten von covey und die
Sitzungen außerhalb, addiert), geteilt durch diese eine echte Kostenzahl: wie
viele Listenpreis-Dollar ein echter Dollar des Abonnements gekauft hat. Nur über
die Ablesungen; Listenpreis aus der Zeit vor der ersten Ablesung des Zählers oder
nach seiner letzten wird gesondert genannt, weil er noch keine echten Kosten hat.
Daneben nennt die Seite eine Untergrenze (*Mindestens …×*): Der Zähler ist auf
±2 Punkte je Platz und 2 mehr je Wochen-Reset bekannt, das Vielfache kann also
nicht kleiner sein als der Listenpreis über den echten Kosten plus diese Spanne.
Ein Vielfaches braucht mindestens 3 Punkte Bewegung und für jeden einen Tarifpreis.

Oben auf der Seite stehen diese als ein Satz in der Karte *Die ganze Delle im
Abo*, sobald eine Quelle von Sitzungen außerhalb mit einem Zähler verknüpft ist:

> Diese ganze Arbeit auf den Abo-Zählern zu Tokenpreisen (wie von Claude Code
> gemeldet): *$X* = covey-Agenten *$A* + Sitzungen außerhalb von covey *$B*,
> gegenüber den echten Kosten des Abos im selben Zeitraum: *$R* (*N* Punkte des
> Zählers). → *$X'* ÷ *$R* ≈ *M*×.

Der Block jedes Abo-Zugangs zeigt dieselben drei für seinen Platz: die führende
Zahl, *Echte Kosten (Schätzung)* und *Listen-$ je echtem $* (mit Sitzungen
außerhalb auf dem Platz *— alles auf diesem Zähler*). Die Kopfzeile folgt dem
Agentenfilter nicht und sagt es.

Wo eine Quelle von Sitzungen außerhalb mit einem Zähler verknüpft ist, aber nicht
über jede Ablesung gemeldet hat, oder keine Quelle verknüpft ist und der Zähler
Nutzung enthalten kann, die niemand meldet, ist das Vielfache von covey eine
Untergrenze (*≥*, *mindestens: Der Zähler kann Nutzung außerhalb von covey
enthalten*), keine Schätzung.

**Wie sich die echten Kosten aufteilen.** Unter der Kopfzeile gibt ein eingeklappter
Abschnitt, *Wie sich die echten Kosten aufteilen (Schätzung)*, den echten Anteil
jeder Partei an: der Agenten von covey, jeder Quelle von Sitzungen außerhalb und
zweier Teile, die niemandem gehören.

- *Nicht zugeordnet* ist Bewegung des Zählers, die weder die Agenten von covey
  noch die gemeldeten Sitzungen erklären, zum Beispiel eine andere Maschine auf
  demselben Konto. Sie wird niemandem gebucht, nie covey.
- *Nicht aufgeteilt* ist Bewegung in Ablesungen, die die Sitzungen außerhalb noch
  nicht gemeldet hatten (oder deren Aufrufe keinen Preis hatten): Sie wird auf
  niemanden aufgeteilt und nicht den Agenten von covey gebucht.

Die Anteile sind Schätzungen, und das Vielfache eines Anteils ist eine
Untergrenze.

**Die andere Zahl.** *Von Anthropic berechnet* ist, was die Usage-and-Cost-API von
Anthropic für den API-Schlüssel hinter den Zugangsdaten meldet, nach Rabatten und
vor Gutschriften. Für eine Rechnung, die nicht gelesen werden konnte, steht nie
0,00 $, sondern der Grund (*kein Anthropic-Admin-Schlüssel hinterlegt*, *keinem
Anthropic-API-Schlüssel zugeordnet*, *der Abgleich der Rechnung schlug fehl*,
*Anthropics Daten sind noch nicht da*). Sie wird nie zu den Zahlen oben addiert;
siehe *Die Rechnung und der Abgleich mit ihr*.

Ein Lauf, der ohne Ergebniszeile stirbt, hat trotzdem Geld gekostet: Zeigt eine
spätere Zeile, dass die laufende Summe von Claude Code um mehr gewachsen ist, als
die Zeilen erklären, legt covey die Differenz als eigene Zeile und eigenen
Kosteneintrag an (nur hinzugefügt, nie abgezogen, und nur, wo beide Enden bekannt
sind). Eine Buchung, mit der keine Zeile verknüpft ist, hat keine Tokens zum
Messen und steckt nicht in den Zahlen; die Seite nennt ihren Betrag (im Diagramm
*Nicht aufgeschlüsselt*), und Buchungen, die ein älterer Daemon aus laufenden
Summen gemacht hat, zählt sie unter *Buchungsprüfung*.

## Sitzungen außerhalb von covey

Claude-Code-Sitzungen, die covey von außen steuern (die eines Operators, die
eines Arbeitsagenten), laufen auf demselben Abo-Zähler oder derselben
Nutzungsrechnung wie die Agenten von covey. covey liest sie nicht selbst: Eine
Maschine schickt ihre Token-Zahlen und sonst nichts mit `covey usage push`
(`--source-per-project`, `--label`, `--host`, `--complete`). Wie man ihn
ausführt, was er sendet und wie die Zusage der Vollständigkeit funktioniert,
steht in [Sitzungen außerhalb von covey](../operations/usage-push.md); bis eine
Quelle gepusht hat, erscheint nichts von ihr.

- **Standardmäßig enthalten, immer gekennzeichnet.** Kacheln, Diagramm, die
  Tabellen je Agent und Modell und die Blöcke der Zugangsdaten zählen die
  Sitzungen außerhalb mit. Jede Zahl nennt ihre zwei Teile (*covey-Agenten $A ·
  Sitzungen außerhalb $B*), und das Diagramm zeichnet jede Quelle benannt und
  schraffiert, markiert *außerhalb von covey*. Mit Agentenfilter lautet eine Zahl
  *gewählte Agenten plus Sitzungen außerhalb*: Die Sitzungen außerhalb sind keine
  Agenten.
- **nur covey.** Der Chip *nur covey* über den Kacheln nimmt sie aus Kacheln,
  Diagramm und Tabellen heraus (`?only=covey`). Er erscheint, wenn mindestens
  eine Quelle Aufrufe im Zeitraum hat. Die Kopfzeile zeigt weiter die ganze Delle.
- **Detailansicht.** Der Abschnitt *Operator-Sitzungen (außerhalb von covey)* zeigt
  sie getrennt oder mit den Agenten zusammen; *Detailansicht →* öffnet
  `/costs/outside`: Listenpreis über die Zeit, je Quelle, je Modell, Hauptsitzung
  gegen Subagenten und Workflows, Tokens je Art mit ihren Dollar und der Anteil
  jeder Quelle am Abo-Zähler. Jede Rolle darf sie lesen.
- **Quellen ohne Zähler.** Eine Quelle auf keinem Abo-Zähler, den die Seite liest
  (Abrechnung je Nutzung, oder verknüpft mit einem Zugang ohne Zeilen oder
  Ablesungen im Zeitraum): Ihre Arbeit steckt in den Kacheln und steht in der
  Kopfzeile gesondert, einen echten Anteil hat sie nicht.
- **Nutzung, die niemand meldet.** Zählernutzung, die keine Quelle meldet, steht als
  *Nicht zugeordnet* da und wird nie covey gebucht (siehe oben).

## Wie die echten Kosten gelesen werden

Ein Claude-Code-Abonnement hat einen Zähler: den bisher verbrauchten Anteil eines
Wochenkontingents, in Prozent. covey liest ihn, nimmt, wie weit er sich bewegt
hat, und bewertet die Bewegung. Bewertet wird nur das Wochenfenster für alle
Modelle; das Fünf-Stunden-Fenster und die Fenster je Modell werden gezeigt und nie
bewertet, weil sie dieselbe Arbeit noch einmal zählen.

- **Ablesungen.** covey fragt Claude Code (`claude -p /usage`) einmal je
  Zugangsdaten, wenn ein Agent darauf aufwacht (die Antwort wird fünf Minuten
  gehalten), und liest das eigene `rate_limit_event` von Claude Code, solange ein
  Lauf streamt. Haben beide dieselben Zugangsdaten gelesen, nimmt die Seite die mit
  mehr Ablesungen und addiert sie nie. Solange kein Agent läuft und covey den
  Zugang nicht selbst abfragt (siehe unten), kommt keine Ablesung: Arbeit nach der
  letzten Ablesung wird gesondert genannt (*nach seiner letzten Ablesung, echter
  Anteil offen, bis der Zähler wieder abgelesen wird*). Der Zähler gehört dem
  *Konto*, eine andere Person oder Maschine darauf bewegt ihn also auch.
- **coveys eigene Abfrage.** Liest kein Lauf einen Abo-Zugang, den Arbeit von
  außen mitbenutzt, liest covey ihn selbst: ein Haiku-Zug über die `claude`-CLI
  mit dem Token des Zugangs, der dasselbe `rate_limit_event` ausgibt. Abgefragt
  werden nur Claude-Code-Zugänge mit einem OAuth-Token von Claude
  (`sk-ant-oat…`); das Abo einer anderen Engine bekommt die CLI nie zu sehen.
  Abgefragt wird ein Zugang nur, wenn eine Quelle von außen mit ihm verknüpft
  ist (die eigene Quelle der Abfrage zählt nicht): Ein Zugang, den keine Quelle
  von außen nennt, hat keine fremde Arbeit, die aufzuteilen wäre, und bleibt
  ganz coveys. Von diesen wird ein Zugang abgefragt, wenn in den letzten 10
  Minuten keine gültige Ablesung von ihm kam (aus dem Lauf oder abgefragt; eine
  von Hand eingetragene zählt nicht) und entweder eine solche Quelle in der
  letzten Stunde gemeldet hat oder die Flotte der Organisation pausiert ist –
  der Flottenschalter ist aus, oder die Organisation hat Agenten und jeder davon
  ist gestoppt. Jeder Prozess der Steuerebene versucht einen Zugang höchstens
  einmal in 10 Minuten; jeder Versuch in Folge, der keine Ablesung ablegt (der
  Aufruf schlug fehl oder gab kein `rate_limit_event` aus), verdoppelt diese
  Wartezeit bis höchstens 6 Stunden, und ein Versuch, der eine ablegt, setzt sie
  zurück. Die Ablesungen werden als `usage_cmd` abgelegt, neben den Antworten
  auf `/usage`. Der Aufruf bewegt den Zähler, den er liest; deshalb wird er als
  Nutzung von außen unter einer eigenen Quelle gebucht, `meter poll:
  <secret>#<slot>` (bei langen Namen des Secrets mit einem Hash gekürzt),
  verknüpft mit dem Zugang, und trägt seinen Anteil am Schritt wie jede andere
  Sitzung; ein Aufruf, dessen Kosten die CLI nicht gemeldet hat, wird nicht
  gebucht, und seine Ablesung wird auch nicht abgelegt. Uploads unter einer
  Quelle, die mit `meter poll` beginnt, werden abgelehnt. Die Abfrage braucht
  die `claude`-CLI auf der Maschine der Steuerebene: `COVEY_METER_POLL` ist
  `auto` (die Vorgabe: `claude` aus dem `PATH`; das Container-Image hat keine,
  dort ist die Abfrage aus, und das Log sagt das einmal beim Start), `off`
  (auch `false`, `no`, `0`) oder der Pfad zum Programm, der einmal beim Start
  geprüft wird. Ist die Abfrage aus, rücken die Wasserstände früher gebuchter
  Abfragequellen trotzdem jede Minute vor, damit sie spätere Ablesungen eines
  Zugangs nie zurückhalten. Die Seite liest weiter eine Ablesequelle je Zugang,
  die mit den meisten Ablesungen; abgefragte Ablesungen zählen also dort, wo sie
  die aus dem Lauf überwiegen.
- **Was ein Punkt wert ist.** Der Preis eines Tarifs wird auf die Wochen seines
  Zeitraums verteilt (ein Monat sind 52/12 Wochen, ein Jahr 52), und ein Punkt des
  Wochenzählers ist ein Hundertstel davon: 200 $ im Monat sind 46,15 $ die Woche,
  0,46 $ je Punkt. Jede Ablesung wird mit dem Tarif bewertet, der galt, als sie
  gelesen wurde. Ein Punkt kostet nur so viel, wenn das Wochenkontingent
  aufgebraucht wird; deshalb ist die Zahl eine Schätzung.
- **Bewegung.** Innerhalb einer Woche steigt der Zähler nur. Die erste Ablesung
  von Zugangsdaten ist ihre Basis und zählt 0; eine Ablesung, deren Rücksetzzeit
  vorrückte, beginnt eine neue Woche und zählt voll.
- **Ganze Prozente, also eine Spanne.** *n* Punkte liegen irgendwo zwischen
  *n* − 1 und *n* + 1, und jede Woche lässt ihr eigenes ±1. 12 Punkte zu 0,46 $
  sind 5,54 $, Spanne 5,08 – 6,00 $.
- **Stündliche Auflösung.** Ein Balken zeigt die Punkte, die in dieser Stunde seit
  der vorigen Ablesung abgelesen wurden. Eine Stunde ohne Ablesung zeigt **0**
  (nicht gelesen, nicht „nichts verbraucht“), und Arbeit erscheint in der Stunde
  der nächsten Ablesung.

**Wann es ein Vielfaches gibt.**

- Ab **3 Punkten** Bewegung, mit einem Tarifpreis für jeden Punkt. Bei 1 oder 2
  Punkten sagt die Seite, wie wenig sich der Zähler bewegt hat, und zeigt nur die
  Spanne. Bei 0 sagt sie, dass sich der Zähler um keinen ganzen Punkt bewegt hat,
  und die Kachel der echten Kosten zeigt eine Obergrenze, *weniger als der Preis
  eines Punkts je Zählerwoche im Zeitraum* (z. B. *< 0,46 $* innerhalb einer
  Woche).
- Ein Zeitraum im Diagramm bekommt eines nach derselben Regel, mit mindestens
  einem Cent Listenpreis und nicht, wo die Zahl unter 0,05× läge; eine einzelne
  Stunde erfüllt das selten.
- Die Schätzung eines Zugangs ist keine Schranke: Andere Nutzung des Kontos macht
  das wahre Vielfache höher, ein nicht aufgebrauchtes Kontingent macht es
  niedriger. Wo Sitzungen außerhalb den Zähler teilen, würde coveys Liste über die
  ganze Bewegung es zu niedrig angeben, also zeigt die Kachel kein Vielfaches nur
  für covey; die Kopfzeile und *alles auf diesem Zähler* geben es.
- Es braucht die ganzen Zugangsdaten: Mit Agentenfilter oder im Layout *Je Agent*
  gibt es keines, weil der Zähler keine Agenten kennt. Die Tabelle zeigt stattdessen
  den *Anteil an echt* jedes Agenten.

## Kostengrundlage: was auf Zugangsdaten das Geld ist

Ein Platz und seine Nutzung können beide Geld kosten. Die *Kostengrundlage* sagt,
was das Geld ist, je Zugangsdaten und ab einem Datum; jeder Zeitraum zählt nach
der Grundlage, die dann galt.

| Grundlage | Das Geld ist | Wirkung |
|---|---|---|
| **Sitzpreis** (Standard) | Der Tarifpreis des Platzes. | Die Bewegung des Zählers wird wie oben bewertet. |
| **Nutzung** | Was der Platz für seine Nutzung berechnet bekam. | Seine Zählerpunkte sind aus den echten Kosten und dem Vielfachen heraus (*Nach Nutzung abgerechnet: Der Zähler ist nicht das Geld*). |
| **Sitzpreis und Nutzung** | Beides. | Der Teil des Platzes kommt aus dem Zähler, die Nutzungsbeträge stehen daneben. Mit einer USD-Zahl für die Beträge und bewerteten echten Kosten zeigt der Block außerdem *Echte Kosten, Sitz + Nutzung*: Sitz plus Nutzung, in Dollar, mit der Unterzeile *Sitz ≈ $X + Nutzung $Y*. Zugangsdaten, deren Grundlage sich im Zeitraum geändert hat, zeigen sie auch. |

Nutzungsbeträge werden von Hand im Block der Zugangsdaten eingetragen
(*Nutzungsbetrag hinzufügen*: Betrag, Währung, von, bis, optional ein USD-Kurs mit
Datum). Ein Betrag behält seine Währung, und eine USD-Zahl und ein Nutzungs-Vielfaches
gibt es nur mit ausdrücklichem Kurs. Das Nutzungs-Vielfache zählt coveys Liste nur
über die Zeiträume, die die gezählten Beträge abdecken (Randabschnitte anteilig
nach Zeit), mit *≥*, wo Sitzungen außerhalb von covey dieselben Zugangsdaten
nutzen. Beträge stehen neben den echten Kosten, je Währung, und werden nie über
Währungen hinweg addiert; zu den echten Kosten des Platzes werden sie nur in der
einen Kachel *Echte Kosten, Sitz + Nutzung* addiert, und nur in USD. Sie zählen nur
für die Zeit, in der die Zugangsdaten eine Nutzungsgrundlage haben. Die Kopfzeile
lässt Nutzungsbeträge weiter aus: Sie nennt sie neben den echten Kosten, in
keiner ihrer Zahlen. Bei einem API-Schlüssel wird die Rechnung abgeglichen und
steht neben ihnen: Ein hier eingetragener Betrag darf nicht dieselbe Rechnung sein,
und sich überschneidende Beträge werden abgelehnt. *Kostengrundlage festlegen* und
*Nutzungsbetrag hinzufügen* dürfen Organisationsadmins und die Sicherheitsrolle;
jede Rolle liest die Zeile *Abgerechnet nach: …* und die Beträge.

## Die Rechnung und der Abgleich mit ihr

Berechnetes Geld wird gelesen, nie gebucht: Die Kosteneinträge von covey bleiben,
was der Daemon gemeldet hat, und die Rechnung wird daneben gespeichert. Einrichten
dürfen es nur Organisationsadmins und die Sicherheitsrolle:

1. Einen Anthropic-Admin-API-Schlüssel (`sk-ant-admin…`) oder einen
   `read:analytics`-Schlüssel unter **Secrets** als `anthropic_admin_key`
   ablegen. Er erreicht nie einen Agenten, und covey lehnt einen Wert
   `sk-ant-admin…` unter jedem anderen Namen ab. Beim Speichern prüft covey ihn
   gegen die Analytics-API von Anthropic.
2. Sagen, welcher API-Schlüssel alle nach Verbrauch abgerechneten Zugangsdaten sind,
   mit `PATCH /api/v1/runtime-instances/{id}/credentials/{ord}/billing-key` und
   `{"api_key_id": "apikey_01…"}` (optional `"workspace_id": "wrkspc_01…"`).
   Dafür gibt es keine Oberfläche. `GET /api/v1/cost/billing-keys` listet die
   Schlüssel, die die Rechnung der letzten 30 Tage nennt. Die Zuordnung folgt dem
   geheimen Wert; ein Abo-Platz hat keinen API-Schlüssel und wird abgelehnt, und
   ein API-Schlüssel gehört immer nur zu einem Zugang.
3. Warten. covey liest die Rechnung einmal pro Stunde (die letzten zwei Tage in
   Stundenabschnitten) und einmal am Tag (35 Tage in Tagesabschnitten, was nachholt,
   was Anthropic korrigiert hat). `POST /api/v1/cost/billing-sync` liest sofort
   (`?full=1` fügt das Nachlesen der 35 Tage hinzu). Ein Lesen, das scheitert,
   behält die letzten guten Zeilen und sagt, warum.

Die Rechnung kommt 4 bis 24 Stunden verspätet und ist daher nur bis zu einem
Zeitpunkt vollständig, den die Seite nennt (*vollständig bis …, der Rest
ausstehend*). Die Kachel summiert ganze Rechnungsabschnitte und stellt die
gemeldeten und nachgerechneten Zahlen genau dieses Zeitraums daneben. *Berechnet /
Liste* ist die Rechnung nach Rabatten geteilt durch den Listenpreis, den Anthropic
für dieselbe Nutzung nennt.

`GET /api/v1/cost/reconcile?days=<n>` vergleicht je UTC-Tag, zugeordneten Zugang und
Modell, was Claude Code gemeldet hat, was die Tabelle von covey aus denselben
Tokens macht (*Liste*) und was Anthropic berechnet hat, mit Token-Zahlen je Art
(`diff` ist berechnet minus gemeldet). Der *Rest* ist der Listenpreis von Anthropic
minus der von covey, und `residual_label` sagt, wie man ihn liest: `match` (unter
einem Cent auseinander), `billed_above_list` (andere Nutzung desselben
Schlüssels, verpasste Tokens oder ein Preis, den die Tabelle nicht kennt),
`billed_below_list` (die Token-Differenzen sagen, welche Art), `incomplete` (der Tag
ist in den eingetroffenen Daten nicht zu Ende), `not_comparable` (eine Zeile außerhalb
von Standard-Tarif, -Geschwindigkeit oder -Geografie) und `no_billed_data`. Ein
*Rabatt* ist keine Lücke. `runs_without_result` zählt Läufe, die ohne
Ergebniszeile gebucht wurden: Ihre Ausgaben stecken in der Rechnung und nirgends
auf der Seite von covey.

## Filter

- **Zeitraum und Abschnitte.** 24 h, 2, 7, 30 oder 90 Tage, mit Abschnitten von 10
  Minuten, einer Stunde, 10 Stunden, einem Tag, einer Woche oder einem Monat. Die
  Wahl eines Zeitraums wählt einen Abschnitt (24 h und 2 Tage stündlich, 7 und 30
  Tage täglich, 90 Tage wöchentlich). Feine Abschnitte gibt es nur bis zu einer
  Grenze: 10 Minuten bis 2 Tage, Stunden bis 7 Tage, 10 Stunden bis 30 Tage; 90
  Tage stündlich wären 2.160 Zeiträume. Zähler und Rechnung werden stündlich
  gelesen, daher sind bei 10 Minuten *Echte Kosten* und *Berechnet* aus, und die
  Seite sagt warum.
- **Agenten.** Chips, in jeder Kombination; *Alle* heißt, dass niemand
  ausgeschlossen ist. Ein Klick fügt einen hinzu oder nimmt ihn heraus, ein
  Doppelklick wählt nur diesen. Die Agenten sind nach Abteilung gruppiert: Die
  Abteilungen sind nach ihren Kosten im Zeitraum geordnet, die Agenten ohne
  Abteilung stehen zuletzt unter *Keine Abteilung*, und die Überschrift einer
  Abteilung wählt die ganze Gruppe aus oder ab. Eine Gruppe zeigt ihre ersten
  fünf Chips und klappt den Rest unter *+N weitere* ein; ein gewählter Chip wird
  nie versteckt. Die Abteilung ist die *jetzige* des Agenten, nicht die zur Zeit
  der Buchung — covey führt keine Geschichte der Wechsel. Hat niemand eine
  Abteilung, ist der Filter eine flache Zeile. Die Sitzungen außerhalb bleiben mit
  Filter dabei, als *plus Sitzungen außerhalb*, außer *nur covey* ist an.
- **Zugangsdaten.** Kein Chip: Das Diagramm hat einen Block je Zugangsdaten mit
  eigenen Kacheln und einen Block für Läufe, die keinen Zugangsdaten zugeordnet
  werden konnten. Über die API lässt sich die Tabelle je Modell mit `?secret_key=`
  auf `GET /api/v1/cost/models` auf einen Zugang eingrenzen.
- **Quelle.** Im Diagramm ein Chip je Zahl: die führende Zahl (*Zu Tokenpreisen
  (wie von Claude Code gemeldet)*), *Echte Kosten* und *Von Anthropic berechnet*,
  standardmäßig alle an, und ein Chip je Quelle von Sitzungen außerhalb. *Mit
  coveys Preistabelle vergleichen* fügt *Aus Tokens nachgerechnet* als eigenen
  Chip hinzu. Echte Kosten und die Rechnung gehören zu ganzen Zugangsdaten, nicht
  zu einem Agenten: Mit Agentenfilter erscheinen sie nur als geschätzter Anteil in
  der Tabelle. Die Tabelle je Modell hat eigene Quellenschalter: *Gemeldet* und
  *Nachgerechnet* sind standardmäßig an, *Echte Kosten* und *Berechnet* aus. Ist
  eine der beiden an, bekommt die Tabelle unter ihrer Summe außerdem eine Zeile je
  Quelle:
  - *Abo-Zugänge, Zählerzeitraum {von} – {bis}* summiert die echten Kosten aller
    Plätze: etwa die Summe (*≈*), mit der summierten Spanne, die die ganzen
    Prozente zulassen. Steht jeder Platz auf 0 Punkten, zeigt sie nur eine
    Obergrenze, *<* die summierten Obergrenzen.
  - *API-Schlüssel, Rechnung {von} – {bis}* summiert alle gemessenen Rechnungen.
  - Beide Zeilen zeigen *–* statt einer Zahl, wenn bei einem ihrer Zugangsdaten
    keine vorliegt; beim Darüberfahren steht, wie viele (*{n} von {m} Zugängen
    ohne Zahl*).
  - Die Zeilen gelten für die ganze Organisation, auch bei Agentenfilter, und
    sind mit *ganze Organisation: folgt dem Agentenfilter nicht* markiert; den
    Anteil des Agenten zeigen sie nicht.
- **Diagramm.** Aufteilen nach Kostenart oder gar nicht (mit *Mit coveys
  Preistabelle vergleichen*); die zu zeichnenden Kostenarten; kombiniert oder *Je
  Agent*; Beträge oder *Echt gegen Liste*, das nur für Abo-Zugangsdaten mit
  Ablesungen gezeichnet wird, bei eingeschalteten echten Kosten, ohne
  Agentenfilter und mit einem Tarifpreis für jeden Punkt.

## Tarifpreis

Im Block von Abo-Zugangsdaten öffnet *Tarifpreis setzen* ein Formular mit dem
Betrag in US-Dollar, dem Zeitraum (pro Monat oder pro Jahr) und *Gültig ab*. Nur
Organisationsadmins und die Sicherheitsrolle sehen es als bearbeitbar.

- *Gültig ab* beginnt am Anfang der laufenden Zählerwoche (das letzte
  Wochen-Zurücksetzen, das die Ablesungen nennen, minus sieben Tage, weitergerückt
  bis zur Woche, die jetzt enthält), oder jetzt, wenn keine Ablesung ein
  Zurücksetzen nennt. Beginnt in dieser Woche schon ein Preis, korrigiert das
  Formular ihn und beginnt jetzt, auf die Minute.
- Ein Preis wird hinzugefügt, nie überschrieben: Der bei *Gültig ab* geltende Tarif
  endet dort, einer, der später beginnt, begrenzt den neuen, und ein zweiter Preis
  zum selben Zeitpunkt wird abgelehnt.
- Er bewertet von da an die Punkte des Zählers und nur die: Nichts richtet sich
  nach ihm, und er ist kein Budget.
- Die API (`PUT /api/v1/runtime-instances/{id}/credentials/{ord}/plan`) nimmt auch
  eine Währung mit ihrem Kurs zu US-Dollar; das Formular kennt nur US-Dollar. Ein
  Tarif in anderer Währung ohne Kurs bewertet nichts.

## Noch nicht abgedeckt

- **Das Vielfache addiert Plätze.** Die echten Kosten der Kopfzeile addieren die
  Zähler jedes Abo-Platzes, den sie lesen kann, eine Zeile je Zähler im
  eingeklappten Abschnitt, und die Zeile *Abo-Zugänge* der Tabelle je Modell (siehe
  *Filter*) summiert ebenfalls Plätze: Zwei Plätze eines Kontos zählen dort jeder
  voll. Die Blöcke der Zugangsdaten addieren nie über Zugangsdaten hinweg.
- **Sitzungen außerhalb kommen per Push.** covey liest keine selbst. Eine Quelle,
  die nicht über eine Zählerablesung hinaus gemeldet hat, lässt diese Ablesung
  nicht aufgeteilt, und eine pausierte Flotte liest selbst keinen Zähler; wo
  coveys eigene Abfrage den Zugang nicht liest (keine `claude`-CLI oder keine
  Quelle von außen verknüpft), wartet seine Bewegung also bis zur nächsten
  Ablesung.
- **Die Zuordnung von Schlüsseln zu Zugangsdaten und der Abgleich haben keine
  Oberfläche.** Beides gibt es nur über die API; die Seite zeigt das Ergebnis der
  Rechnung in der Kachel *Von Anthropic berechnet*.
- **Ablesungen nur für Claude Code.** Andere Engines haben keine Zählerablesung,
  also keine echten Kosten.

## Weiter

- [Sitzungen außerhalb von covey](../operations/usage-push.md) — `covey usage push`, eine Quelle je Projekt, Labels und die Zusage der Vollständigkeit
- [Runtimes und Engines](runtimes.md) — Zugangsdaten, Plätze und was jede Engine meldet
- [Identität & Secrets](identity-and-secrets.md) — wo Zugangsdaten oder der Admin-Schlüssel abgelegt werden
- [Guard-Rails](guard-rails.md) — das Budget je Agent, eine andere Zahl als diese
