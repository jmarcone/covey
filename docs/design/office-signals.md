# Office signals — one label per thing, linked highlight, one legend line

Part D of #122. Spec for covey-frontend (`#122 implement 3d-office 4/4`). Built on main `ebdcff35`; it assumes part C's fit (whole floor visible, even margin) and part B's render-on-demand loop.

**The question the office answers:** *who is working right now, where do they sit, and who is waiting for me?* The lists above the office answer *what*. The office answers *where*, and nothing in it may say a second thing in a second voice.

## 1. Labels: one per thing (D.1)

| Thing | Label | Shown |
|---|---|---|
| Department room | one sign: colour dot + department name | always |
| Common room (meeting room, kitchen, lounge) | one sign: name only, no dot | only while the room is hovered or focused (§3) |
| Person | one name label under the head (first name, as today) | always, unless it yields (§2) |
| Person with the card open | the card; the bubble and the name label are hidden while the card is open | while open |

**Why there are two “Lounge” signs:** `plan.ts:674-700` (`zeileFuellen`) adds up to `b.length + 1` lounges to each row once the free space reaches `LOUNGE_MIN`. Every lounge is a `Raum` of its own (`id` `lounge-<n>`, name `team.raumLounge`), and `Buero.tsx:527` draws a sign for every `Raum`. Each lounge stays its own room, but common rooms lose their permanent sign. Their furniture says what they are, and the department signs are what you look for. The implementer confirms this cause in the PR.

## 2. Collision rules (D.2)

Each item on the overlay has a box in screen pixels. The box comes from sizes that are measured once per content change or resize (part B: no layout reads in the frame loop). Two visible boxes may never intersect. A 4 px gap is required.

**Priority, highest first.** On a conflict, the item that is lower in this list yields:

1. **The open card** (`BuKarte`). It never yields.
2. **The highlighted figure** (§4), plus the hovered or focused figure: its head, full bubble and name.
3. **Faces (heads).** They never move and are never hidden. They are what the room is about.
4. **“Waiting for you” bubbles.** These ask for action, so they never fold. When two of them collide (the queue at the front desk), they merge into one bubble on the figure nearest the camera (largest screen y). That bubble reads `{{count}} waiting for you` (new key `team.wartenAnzahl`, plural forms, used only when count ≥ 2).
5. **Department signs.** They are pushed apart as today (`schilderStellen`). They move **upwards only**, at most 48 px from their anchor, and the mast keeps the link to the room. If a sign still collides at 48 px, its name truncates with an ellipsis at a 120 px maximum. It is never hidden.
6. **Names.** On a collision, the name of the figure further back (smaller screen y) is hidden. A working figure's name beats an idle or sleeping one, whatever the depth. A hidden name comes back while its figure is hovered, focused or highlighted.
7. **Step bubbles** (“is thinking”, “is using a tool” …). These yield first. Stage 1: the bubble folds into a 22 × 14 px `…` pill with the same style. Stage 2: if the pill still collides, it is hidden. The full step text is never lost: it stays in the “Running now” row, in the card, and in the figure's `title` / accessible name.

**Motion rules:**
- A figure in motion (`f.geht`) shows no bubble, and its name yields to everything. The bubble appears once it stops. This is what keeps folds from flickering while ambient life runs.
- A fold, merge or hide holds for at least 600 ms before it may revert, so nothing flaps at a boundary.
- Collisions are worked out again on camera change, resize, data change and when a figure stops, not on every animation frame.

**Check sizes:** 1280 × 800 and 1920 × 1080, light and dark, with two orgs:
- (a) the demo seed (`demo/seed`, 10 agents in 3 departments);
- (b) a stress fixture with 70 agents in 8 departments, where all 70 have a step and 5 are waiting. That is the head count at which `gesichtGroesse` drops to 24.

## 3. Room numbers on hover or focus only (D.3)

The sign shows the name only. The number `<corridor + 1>.<room nr, 2 digits>` (`flur + 1`, `nr`, as `Buero.tsx:539` builds it today) is appended as ` · 1.02` in `--text-muted` with tabular figures, and only while its room is:
- **hovered**: the pointer is over the room's floor rectangle, hit-tested in plan coordinates, or over its sign; or
- **focused**: keyboard focus is on a figure that sits in that room, or the room holds the highlighted figure (§4).

A common room's sign (§1) appears under the same condition, without a number, because common rooms have none. Only one room is in this state at a time.

## 4. Linked highlight: “Running now” ⇄ office (D.4)

There is one shared state, `hervor: agent id | null`, lifted into `Ueberblick` and passed to `Buero`.

**What sets it:**
- pointer-enter on a row, or focus on a row;
- pointer-enter on a figure, or focus on a figure;
- the pointer over a lit desk. Raycast against the workplace the way plants are found today; the workplace carries the agent id in `userData`.

**What clears it:** leave or blur. After a leave, it falls back to whatever element still has keyboard focus. The last event wins.

**Rules:**
- An agent with two running tasks has two rows, and both are highlighted.
- Hovering an idle or sleeping figure changes nothing in the list, because there is no row for it.
- **The camera never moves** because of the highlight.

```
Running now                                        office
┌───────────────────────────────────────────┐     ┌─ Support ───────────────┐
│▌(face) Refund for Meridian       12 min   │ ⇄   │   [is thinking]         │
│        Ada · is thinking                  │     │      (Ada)   ← lifted   │
└───────────────────────────────────────────┘     │   ◯══════◯  ← floor ring│
 border + tint in accent, no outline               └─────────────────────────┘
```

| Side | Highlight look | Kept apart from |
|---|---|---|
| Row (`.tm-laeuft-zeile.hervor`) | `border-color: var(--text-accent)`, `background: var(--accent-tint)` | focus: 2 px accent **outline**, offset 2 (stays on top when the row is focused); “just added”: inset 3 px edge |
| Figure (`.bu-wer.hervor`) | head lifted as on hover (`translateY(-2px) scale(1.1)`); name `--text-primary`, weight 600; the floor shadow (`::after`) becomes a 34 × 12 ellipse, `2px solid var(--text-accent)` on `var(--accent-tint)`, no blur; `z-index: 5` (above other figures, below the card); full bubble (rule 2) | card open (`.gewaehlt`): accent-coloured name, head scaled 1.16 |
| Figure on another floor | that floor's button in `.bu-etagen` gets a 6 px accent dot at its top-right. The floor does **not** switch. | `aria-pressed` fill (current floor) |
| Room of the figure | its number shows (§3) | `.meins` ring (your own room) |

Under `prefers-reduced-motion`, the same states appear without transitions.

## 5. Legend: one line (D.5, D.6)

The sentence `team.bueroLegende` (`Buero.tsx:665`) is replaced by one line under the stage. It keeps its key, with new copy in all 10 locales.

```
■ Glowing desk: a task is running there
```

- **Swatch:** 10 × 10 px, radius 2, fill `var(--office-screen-lit)`, 1 px `var(--border-strong)` border. The border lets it hold up on light `--surface-0`, where the fill alone is under 3:1.
- **Text:** 12 px, `--text-muted`, one line, ellipsis, no wrap.
- **English copy (key `team.bueroLegende`):** `Glowing desk: a task is running there`.
- **New token:** `--office-screen-lit: #ffb268`, the same value in both themes. It is exactly `FARBEN.schirmAn` (`buero/mal.ts:51` and `:68`), and a comment ties the two together. This is the only new colour.

**Wait colour in the “Running now” pill.** `.tm-laeuft-dauer` (`app.css:3219`) paints a running duration in wait-blue (`--bg-wait` / `--text-wait`), while the office uses that same blue for “Waiting for you”. That gives one colour two meanings on one screen. The pill becomes neutral: `background: var(--surface-2); color: var(--text-secondary)`.

**Colour code on this screen:**
- amber `--office-screen-lit` / scene glow = a task is running;
- wait-blue = waiting for you (bubble only);
- accent = pointed at: outline = keyboard focus, border + tint + floor ring = linked highlight, bold accent name = card open, sign ring = your room. Shape tells these apart.
- Department colour = which department (sign dot only).

## 6. Strings

- **Changed:** `team.bueroLegende`.
- **New:** `team.wartenAnzahl_one` / `_other` (“{{count}} waiting for you”). The `_one` form is never shown, but it is required by the plural rules.

Both go into all 10 locales. Nothing is hard-coded. The `…` pill is a glyph and is `aria-hidden`.

## 7. Acceptance criteria (reviewer can check)

1. With default view, at both sizes and both orgs, every department room has exactly one sign. No common-room sign shows until its room is hovered or focused, and no two “Lounge” signs are ever visible together.
2. No visible bubble, name, sign or `…` pill intersects another one (4 px gap) at 1280 × 800 and 1920 × 1080 with orgs (a) and (b), in all four rotations. A unit test over the collision function asserts this for fixture (b).
3. Heads and “Waiting for you” bubbles are never hidden. Colliding waiting bubbles merge into one bubble with a count.
4. No room number is visible by default. Hovering a room's floor or tabbing to one of its figures shows ` · 1.02` on exactly that sign.
5. Hovering or focusing a row highlights the figure (floor ring, lifted head, bold name), and the reverse also works. This includes a lit desk with no figure button under the pointer. Tab order alone is enough to see it. An agent on another floor marks its floor button with the dot and the floor does not change. A test covers the shared `hervor` state in both directions.
6. The highlight differs from focus (no outline), from the open card (name not accent-coloured) and from “just added” (no inset edge).
7. Exactly one legend line, swatch plus copy, under the stage. The `.tm-laeuft-dauer` pill is no longer blue.
8. Every changed or new key is present in all 10 locales (parity test green) and there is no English left in the non-English files. The only new colour is `--office-screen-lit`.
9. Screenshots in the PR at both sizes, light and dark, plus one each with a row hovered and with a figure hovered.

## Not settled here

- The pill's number is `now − tasks.updated_at` (`internal/httpapi/running.go:43`). `set_stage` moves that value too, so it measures time since the last change, not run time. That is a labelling question for the pill, outside part D.
- Linking the “Waiting for you” list to figures the same way is possible later, but it is not part of D.4.
