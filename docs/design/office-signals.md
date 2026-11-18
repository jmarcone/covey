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

Each item on the overlay has a box in screen pixels. The box comes from sizes that are measured once per content change or resize (part B: no layout reads in the frame loop). Two visible boxes of standing figures may never intersect. A 4 px gap is required.

**Bubbles are classed by state, not by text.** A figure whose state is `wartet` shows “Waiting for you” (`team.wartet`), even when it also has a running step. Today `zustandVon` already lets `wartet` win over `arbeitet` (`Buero.tsx:160`), so its desk stays dark (`szene.ts:492`), but `Buero.tsx:572` shows the step and hides the waiting bubble. That condition goes. The button's state class follows `zustand` too (`.wartet`, not `.arbeitet` from `laeuft`). The step stays in its “Running now” row, and that row's highlight (§4) points at the figure in the queue, although no desk is lit.

**Priority, highest first.** On a conflict, the item that is lower in this list yields:

1. **The open card** (`BuKarte`). It never yields.
2. **The highlighted figure** (§4), plus the hovered or focused figure: its head, full bubble and name, also while it walks.
3. **Faces (heads).** They are never hidden. They move only with their figure.
4. **Bubbles of `wartet` figures.** These ask for action, so they never fold and are never hidden, also while the figure walks. When two of them collide among standing figures (the queue at the front desk), they merge into one bubble at the figure nearest the camera (largest screen y). That bubble reads `{{count}} waiting for you` (new key `team.wartenAnzahl`, used only when count ≥ 2). It is drawn in the overlay outside every button, `aria-hidden`, and positioned per frame at that head. The merged figures show no bubble of their own.
5. **Department signs.** They are pushed apart as today (`schilderStellen`). They move **upwards only**, at most 48 px from their anchor, and the mast keeps the link to the room. If a sign still collides at 48 px, its name truncates with an ellipsis at a 120 px maximum. It is never hidden.
6. **Names.** On a collision, the name of the figure further back (smaller screen y) is hidden. A working figure's name beats an idle or sleeping one, whatever the depth. A hidden name comes back while its figure is hovered, focused or highlighted.
7. **Step bubbles** of `arbeitet` figures (“is thinking”, “is using a tool” …). These yield first. Stage 1: the bubble folds into a 22 × 14 px `…` pill with the same style. Stage 2: if the pill still collides, it is hidden. The full step text is never lost: it stays in the “Running now” row, in the card and in the button's `aria-label` (below).

**Motion rules.** A figure is moving while `f.geht` is true. The frame loop already toggles `.geht` on its button (`Buero.tsx:327`) and writes its position every frame (`:324-326`).
- A moving figure's step bubble and name are hidden until it stops. Exceptions: the bubble of a `wartet` figure (rule 4) and everything of the figure in rule 2 stay visible and move with the button, so they are positioned per frame at no extra cost.
- Moving items (head, and the bubbles that stay) are exempt from the gap rule until the figure stops. They may pass over names and bubbles of standing figures. Nothing yields to them mid-walk, and nothing is rechecked per frame.
- A fold, merge or hide holds for at least 600 ms before it may revert, so nothing flaps at a boundary. The collision function takes the time as an argument (`jetzt` in ms); its tests pass the time, and none waits on the wall clock.
- Collisions are worked out again on: camera change, resize, data change, a figure starting or stopping (`geht` flips), a change of `hervor` (§4, rule 2 brings back a hidden name), and the hovered or focused room changing (§3, the sign widens by ` · 1.02`). Not on every animation frame.

**Accessible name.** Every figure button gets a stable `aria-label`, `team.figurAria` = `{{name}}, {{zustand}}`: the full `display_name`, then `team.wartet` for `wartet`, or the step text (`team.schritt.<step>`) for `arbeitet` with a step. Without either, the label is the `display_name` alone. The label does not depend on folding, merging or hiding, and it contains the visible first name, since the name label is the first word of `display_name` (`Buero.tsx:584`). `title` stays as it is and becomes the description.

**Check sizes:** 1280 × 800 and 1920 × 1080, light and dark, with two orgs:
- (a) the demo seed (`demo/seed`, 10 agents in 3 departments);
- (b) a stress org with 70 agents in 8 departments, 5 of them waiting. That is above the 40 at which `gesichtGroesse` drops to 24 (`Buero.tsx:503`). `/org/running` returns at most 20 rows (`running.go:50`, `LIMIT 20`), so the live screenshots show 20 steps and lit desks. The unit test of the collision function gives all 70 a step.

## 3. Room numbers on hover or focus only (D.3)

The sign shows the name only. The number `<corridor + 1>.<room nr, 2 digits>` (`flur + 1`, `nr`, as `Buero.tsx:539` builds it today) is appended as ` · 1.02` in `--text-muted` with tabular figures, and only while its room is:
- **hovered**: the pointer is over the room's floor rectangle, hit-tested in plan coordinates, or over its sign; or
- **focused**: keyboard focus is on a figure whose **seat** is in that room (`r.leute`), or that room holds the highlighted figure's seat (§4).

“The room of a figure” always means its seat's room, wherever the figure stands (kitchen, queue). If the seat's room is on a floor that is not shown, no number shows. A common room's sign (§1) appears under the same condition, without a number, because common rooms have none, and nobody's seat is in one, so only the pointer brings it up. Only one room is in this state at a time. The last event wins (pointer, focus or `hervor`, as in §4). When it ends, the state falls back to a source that is still active, focus first.

## 4. Linked highlight: “Running now” ⇄ office (D.4)

There is one shared state, `hervor: agent id | null`, owned by `Ueberblick`. It is not React state that re-renders `Buero`: `Ueberblick` holds it in a ref with a subscribe function and passes that once. `Buero` subscribes in an effect, toggles `.hervor` on the buttons through `knoepfe`, and wakes exactly one frame of part B's on-demand loop per change. The row list (≤ 20 rows) may re-render. `Buero` keeps working without the prop (`Team.tsx:315` renders it too).

**What sets it:**
- pointer-enter on a row, or focus on a row;
- pointer-enter on a figure, or focus on a figure;
- the pointer over a lit desk. In the `arbeitsplatzBauen` loop (`szene.ts:491-497`), each lit screen gets `userData.agent = agent.id`, only when `an`. Pointer moves raycast for it the way `aufKlick` finds plants (`Buero.tsx:432`), at most once per frame. The lounge's arcade screen (`katalog/lounges.ts:212`) glows without an agent, carries no id and never hits.

**What clears it:** leave or blur. After a leave, it falls back to whatever element still has keyboard focus. The last event wins.

**Rules:**
- An agent with two running tasks has two rows, and both are highlighted.
- Hovering an idle or sleeping figure changes nothing in the list, because there is no row for it.
- **The camera never moves** because of the highlight.
- Where the figure is: the floor ring is drawn where it currently stands (`f.hier`, `f.pos`). The floor dot follows `f.hier`. The room number follows the seat (§3).

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
| Row (`.tm-laeuft-zeile.hervor`) | `border-color: var(--text-accent)`, `background: var(--accent-tint)` | focus: 2 px accent **outline**, offset 2 (stays on top when the row is focused); hover: `--border-strong` (`app.css:3207`) |
| Figure (`.bu-wer.hervor`) | head lifted as on hover (`translateY(-2px) scale(1.1)`); name `--text-primary`, weight 600; the floor shadow (`::after`) becomes a 34 × 12 ellipse, `2px solid var(--text-accent)` on `var(--accent-tint)`, no blur; `z-index: 4` (above other figures, below the card's figure, `.gewaehlt` = 5, `app.css:3425`); full bubble (rule 2) | card open (`.gewaehlt`): accent-coloured name, head scaled 1.16 |
| Figure on another floor (`f.hier` ≠ shown floor) | that floor's button in `.bu-etagen` gets a 6 px accent dot at its top-right. The floor does **not** switch. | `aria-pressed` fill (current floor) |
| Room of the figure's seat | its number shows (§3) | `.meins` ring (your own room) |

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

**Wait colour in the “Running now” pill.** `.tm-laeuft-dauer` (`app.css:3219`) paints a running duration in wait-blue (`--bg-wait` / `--text-wait`), while the office uses that same blue for “Waiting for you”. That gives one colour two meanings on one screen. The pill becomes neutral: `background: var(--surface-2); color: var(--text-secondary)`. The rule keeps one `color` line: the first one (`app.css:3223`) is deleted, since today it is overridden four lines further down.

**Colour code on this screen:**
- amber `--office-screen-lit` / scene glow = a task is running;
- wait-blue = waiting for you (bubble only);
- accent = pointed at: outline = keyboard focus, border + tint + floor ring = linked highlight, bold accent name = card open, sign ring = your room. Shape tells these apart.
- Department colour = which department (sign dot only).

## 6. Strings

- **Changed:** `team.bueroLegende`.
- **New:** `team.wartenAnzahl` (“{{count}} waiting for you”) in the CLDR plural forms of each locale: `_one` / `_other` in English, `_one` / `_few` / `_many` / `_other` in Polish as `chart.agents_*` already has (`pl.json:1355-1358`). The `_one` form is never shown, but the plural rules need it.
- **New:** `team.figurAria` (“{{name}}, {{zustand}}”), the figure button's label (§2).

All go into all 10 locales. Nothing is hard-coded. The `…` pill is a glyph and is `aria-hidden`.

## 7. Acceptance criteria (reviewer can check)

1. With default view, at both sizes and both orgs, every department room has exactly one sign. No common-room sign shows until its room is hovered or focused, and no two “Lounge” signs are ever visible together.
2. No visible bubble, name, sign or `…` pill of a standing figure intersects another one (4 px gap) at 1280 × 800 and 1920 × 1080 with orgs (a) and (b), in all four rotations. A unit test over the collision function asserts this for fixture (b) (70 steps, 5 waiting), with the time passed in; a second case shows a fold holding for 600 ms and reverting after it.
3. Heads and the bubbles of `wartet` figures are never hidden, also while the figure walks to the front desk. Colliding waiting bubbles merge into one `aria-hidden` bubble with a count. A figure that is waiting and has a running task shows “Waiting for you”, and its row highlights it in the queue.
4. No room number is visible by default. Hovering a room's floor or tabbing to a figure seated there shows ` · 1.02` on exactly that sign, also while that figure stands elsewhere on the same floor.
5. Hovering or focusing a row highlights the figure (floor ring, lifted head, bold name), and the reverse also works. This includes a lit desk with no figure button under the pointer; the arcade screen never highlights. Tab order alone is enough to see it. An agent on another floor (`f.hier`) marks its floor button with the dot and the floor does not change. A test covers the shared `hervor` state in both directions, and that a row hover does not re-render `Buero`.
6. The highlight differs from focus (no outline), from row hover (accent, not `--border-strong`) and from the open card (name not accent-coloured; the card's figure stays on top).
7. Every figure button has an `aria-label` with the full name and its state. It is unchanged when its bubble folds, merges or hides, or its name label hides.
8. Exactly one legend line, swatch plus copy, under the stage. The `.tm-laeuft-dauer` pill is no longer blue and has one `color` declaration.
9. Every changed or new key is present in all 10 locales with each locale's plural forms (parity test green), and there is no English left in the non-English files. The only new colour is `--office-screen-lit`.
10. Screenshots in the PR at both sizes, light and dark (org (b) live: 20 running), plus one each with a row hovered and with a figure hovered.

## Not settled here

- The pill's number is `now − tasks.updated_at` (`internal/httpapi/running.go:43`). `set_stage` moves that value too, so it measures time since the last change, not run time. That is a labelling question for the pill, outside part D.
- Linking the “Waiting for you” list to figures the same way is possible later, but it is not part of D.4.
