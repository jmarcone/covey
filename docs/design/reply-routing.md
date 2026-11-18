# Design: a routed message says where it went

Issue #22, scope item 4 (could). Builds on item 1 (`feat/reply-resumes-open-task`): a chat message
that answers an agent's message about a task becomes a note on that task and resumes it, instead
of a new task.

**The person's question:** "Where did my message go — and if covey got it wrong, how do I
make it the new request I meant?" The misrouting case this guards: a request typed under an agent's
question about something else (a run waiting on a customer, say) is taken as the answer.

## 1 · What the bubble shows

Only on the person's **own** message (`.tm-blase.ich`) that covey routed **by itself**. A message
sent with the explicit *Answer* button (`antwortZustellen`, chat.go:1752) is not marked: the person
chose that task.

```
                                   ┌──────────────────────────────────────┐
                                   │ please also send Initech the invoice │  ← bubble, unchanged
                                   └──────────────────────────────────────┘
                     ↳ Added to Reply to ACME about the delivery date · Make it a new task instead
                                                                                       10:42
```

- One quiet line **under** the bubble text, outside the filled accent surface, aligned to the bubble's
  end edge (as `.tm-files`): `↳` (aria-hidden) · *Added to* · **task title as a link** · `·` · button.
- Title: 12 px, `--text-secondary`, underline on hover/focus, the link style of `.tm-vorgang-titel`
  (max 46ch, ellipsis, full title in `title`). The rest of the line: 12 px `--text-muted`.
- Button: the quiet text button of `.tm-antworten` (no frame, `--text-muted`, underline on hover),
  `<button type="button">`. It is for the author only and only while the composer may write
  (`darfSchreiben`, agent not stopped). Everybody else in the conversation sees the line without it.
- Narrow (≤ 480 px): the line wraps; the button goes onto its own line, end-aligned, never splits.
- **Link target:** the task's page, `/agents/{agent_id}?task={task_id}` — the same target as the
  task divider (Thread.tsx:526). Thread knows the agent; Gespraech reads it from meta `answer_agent`
  (without it, the title is plain text, no link).
- **Colour:** no new colour, no state colour. Routing is a fact about the past, not a state, a warning
  or a wait: blue (`--bg-wait`) stays "waiting for someone", amber stays warnings. Error text only uses
  `--text-danger`, as `.tm-fehler`.

Both surfaces: `team/Thread.tsx` (direct, plain message under the agent's last task message) and
`team/Gespraech.tsx` (reply-to on any agent message). Same component, e.g. `team/Weitergeleitet.tsx`.

## 2 · Button states

| State | Line reads | Button | Focus / screen reader |
|---|---|---|---|
| idle | ↳ Added to *title* · Make it a new task instead | enabled | label as shown |
| pending | ↳ Added to *title* · Making a new task … | disabled, `aria-busy` | stays on button |
| done | ↳ New task: *new title* (link to the new task) | gone | focus moves to the new link; line in `role="status"` |
| error | idle line + below: "No new task was made: *server error*" (`--text-danger`, `role="alert"`) | enabled again | stays on button |
| 409 not routed | no error: refetch the thread; the server's state shows (done line) | — | — |

The done line persists after a reload: it is drawn from meta `split_from` (below), not local state.
Untitled task (redacted, or no title): "Added to an open task", linked where the id is known.
No time limit: the button stays while the marking does, even if the task has since closed.

## 3 · Strings (`chat.routed.*`, all ten locales)

`{{title}}` is rendered as the link: split the translated string at the placeholder so each locale
keeps its word order (no `<Trans>` in the codebase).

| key | en | de |
|---|---|---|
| addedTo | Added to {{title}} | Zu {{title}} hinzugefügt |
| addedToUntitled | Added to an open task | Zu einer offenen Aufgabe hinzugefügt |
| makeTask | Make it a new task instead | Stattdessen eine neue Aufgabe daraus machen |
| making | Making a new task … | Neue Aufgabe wird angelegt … |
| madeTask | New task: {{title}} | Neue Aufgabe: {{title}} |
| failed | No new task was made: {{error}} | Keine neue Aufgabe angelegt: {{error}} |
| hint (`title` on the line) | covey took this message as your answer to that task and added it there as a note. | covey hat diese Nachricht als deine Antwort auf diese Aufgabe genommen und dort als Notiz angefügt. |

| key | es | fr | it |
|---|---|---|---|
| addedTo | Añadido a {{title}} | Ajouté à {{title}} | Aggiunto a {{title}} |
| addedToUntitled | Añadido a una tarea abierta | Ajouté à une tâche ouverte | Aggiunto a un compito aperto |
| makeTask | Convertirlo en una tarea nueva | En faire plutôt une nouvelle tâche | Farne invece un nuovo compito |
| making | Creando una tarea nueva … | Création d’une nouvelle tâche … | Creazione di un nuovo compito … |
| madeTask | Tarea nueva: {{title}} | Nouvelle tâche : {{title}} | Nuovo compito: {{title}} |
| failed | No se creó ninguna tarea: {{error}} | Aucune tâche créée : {{error}} | Nessun compito creato: {{error}} |
| hint | covey tomó este mensaje como tu respuesta a esa tarea y lo añadió allí como nota. | covey a pris ce message pour ta réponse à cette tâche et l’y a ajouté comme note. | covey ha preso questo messaggio come tua risposta a quel compito e l’ha aggiunto lì come nota. |

| key | nl | pl | pt |
|---|---|---|---|
| addedTo | Toegevoegd aan {{title}} | Dodano do {{title}} | Adicionado a {{title}} |
| addedToUntitled | Toegevoegd aan een open taak | Dodano do otwartego zadania | Adicionado a uma tarefa aberta |
| makeTask | Maak er liever een nieuwe taak van | Zamiast tego utwórz nowe zadanie | Fazer antes uma tarefa nova |
| making | Nieuwe taak wordt gemaakt … | Tworzenie nowego zadania … | A criar uma tarefa nova … |
| madeTask | Nieuwe taak: {{title}} | Nowe zadanie: {{title}} | Tarefa nova: {{title}} |
| failed | Geen nieuwe taak gemaakt: {{error}} | Nie utworzono zadania: {{error}} | Nenhuma tarefa criada: {{error}} |
| hint | covey nam dit bericht als je antwoord op die taak en voegde het daar toe als notitie. | covey uznał tę wiadomość za odpowiedź na to zadanie i dodał ją tam jako notatkę. | O covey tomou esta mensagem como a tua resposta a essa tarefa e juntou-a lá como nota. |

| key | ja | zh |
|---|---|---|
| addedTo | {{title}} に追加しました | 已添加到 {{title}} |
| addedToUntitled | 進行中のタスクに追加しました | 已添加到一个未完成的任务 |
| makeTask | 代わりに新しいタスクにする | 改为新建任务 |
| making | 新しいタスクを作成中 … | 正在新建任务 … |
| madeTask | 新しいタスク: {{title}} | 新任务：{{title}} |
| failed | 新しいタスクは作成されませんでした: {{error}} | 未能新建任务：{{error}} |
| hint | covey はこのメッセージをそのタスクへの返事と判断し、メモとして追加しました。 | covey 把这条消息当作你对该任务的答复，并作为备注添加到了那里。 |

## 4 · Contract (covey-dev)

**Marking, written by item 1** on the routed message's Meta, next to the `answer_to` it already sets:
`routed` = `reply` (reply-to) | `last` (plain message under the agent's last task message), and
`answer_agent` = the task's agent id. `antwortZustellen` writes neither. #638's routing to a blocked
question (`offeneFrage`, chat.go:793) is automatic too and gets `routed` as well.

**Thread entries** (`chatEntry`, chat.go): add `message_id` (the message's own id; `id` is the task's
when there is one), `routed` (meta `routed`, omitempty) and `split_from` (meta `split_from`, omitempty).
Gespraech already receives `meta`.

**`POST /api/v1/conversations/{id}/messages/{message}/task`** (`conversationScoped`, beside
`…/messages/{message}/spoken`). Body: none. Language: `Accept-Language` (`langFrom`).

1. 404 unless the message is in conversation `{id}`; 403 unless its author is the caller
   ("only who wrote the message makes it a task"); 409 via `gestoppt` if the task's agent is stopped.
2. **Exactly once:** clear the marking with a conditional update — remove `routed`, `answer_to`,
   `question`, `answer_agent`, set `split_from` = the old task id — `WHERE id = $1 AND meta ? 'routed'`.
   No row → **409** `{"error":"the message is not routed (any more)","task_id":"<its current task>"}`.
3. `aufgabeAusNachricht(ctx, conv, agentID, msg, chat.Entscheidung{}, msg.ForAgent(), email, lang, true)`
   — the task a message gets without triage; `LinkTask` moves the message's `task_id` to it. No triage:
   the person asked for a task.
4. A note on the **old** task, author `human:<email>`: "Not an answer to this task: <email> made this
   message a new task, <title> (<id>). Do not act on it here." The first note and the resume stay (non-goal);
   this note is what lets the run decide.
5. `chatEreignis(org, agent, conv, "task", {"task_id": new})`.

Steps 2–3 in one transaction, or 3 failing puts the marking back: a failed call leaves the button.
**201** `{"message": <ConversationMessage, task_id = new task, meta with split_from>, "task": <backlog.Task>}`.

## 5 · Acceptance criteria

1. A message routed by item 1 (reply-to and plain-last) shows "Added to *title*" under it in Thread
   and Gespraech; the title links to `/agents/{agent}?task={task}`. An *Answer*-button reply shows no line.
2. Only the author sees the button; a stopped agent or a reader without write access sees the line only.
3. One click creates exactly one task with the message text (double-click, two tabs: still one; the
   second call gets 409 and the UI shows the done line, no error). The message moves to the new task.
4. After success the button is gone, the line reads "New task: *title*" with a link, also after a
   reload; focus lands on that link.
5. A server error shows the danger line with the server's text; the button works again.
6. The old task has the original note plus the "Not an answer to this task" note; its state is untouched.
7. Light and dark: no new colour token; contrast on `--surface-0` (light/dark): muted 6.1/6.8, secondary 8.1/9.1,
   danger 6.9/7.9 — all ≥ 4.5:1. Keyboard:
   Tab reaches link then button, Enter/Space activates. 390 px: no overflow, button on its own line.
8. `chat.routed.*` in all ten locales with the same keys (`parity.test.ts` green); no literal strings.
9. Tests: Go integration test for 201/403/404/409 and exactly-once; vitest for idle/pending/done/error.
