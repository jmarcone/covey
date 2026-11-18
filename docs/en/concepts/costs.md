---
slug: costs
title: Cost & Tokens
description: 'What the four figures on the cost page mean and when each one exists, how the real cost of a subscription is estimated from the provider''s meter, how the bill of an API key is read and reconciled, and what the page does not count yet.'
faq:
  - q: Why does the page show several different dollar figures for the same work?
    a: 'They answer different questions. Reported by Claude Code is the engine''s own price of its lines, Recomputed from tokens is the same tokens priced with covey''s table, Real cost (estimate) is what a subscription seat used of its weekly allowance, and Billed by Anthropic is what an API key was charged. They are shown per credential and never added up across credentials.'
  - q: Why does an hour show 0 real cost when agents were working?
    a: 'The real cost comes from the provider''s meter, and covey reads the meter only when an agent wakes on that credential or while a run streams. A bar shows the points read in that hour since the previous reading; an hour without a reading shows 0, and work from an earlier hour can appear in a later one. Over a longer range the movement adds up, to within one point per week.'
  - q: Why is there no "list per real" multiple for a single hour or for one agent?
    a: 'The meter moves in whole percents, so a few points are known only to within one point, and the meter covers the whole account, not one agent. The multiple is shown from 3 points of movement, over a range, for a whole credential.'
  - q: Where do I set what the subscription costs?
    a: 'On the cost page, on the credential''s block: Set plan price. An organisation admin or a security role can do it. It prices the meter''s points read from its *Valid from* on, never before.'
  - q: Does the page count my own Claude Code sessions outside covey?
    a: 'Not yet. Only runs of covey''s agents are in any figure. Sessions that drive covey from outside still move a shared subscription''s meter.'
---

The cost page (*Costs* in the *Admin* column of the sidebar; the page is headed *Cost & Tokens*) shows what the agents' work
consumed. Every role can read it; setting a plan price is for organisation
admins and the security role. All periods are cut in UTC: UTC days, weeks that
begin on Monday UTC.

The page carries up to four dollar figures for the same work. They come from
four sources, they are kept apart, and the page never adds one to another —
nor the figures of two credentials to each other. A *credential* is the secret
value a run went out on (an API key, or a subscription token), and its kind
says what money means on it.

## The four figures

| Figure | What it is | It exists when | It does not exist when |
|---|---|---|---|
| **Reported by Claude Code** | Claude Code's own price of its lines: the sum of the `costUSD` it writes on each result line, one line's own share at a time. Not covey's booking. | The run is on the Claude Code engine and its result line carries usage per model. | The engine writes no such line (Codex reports tokens and no money; see [Runtimes](runtimes.md)). A run that was cut off before its result line is covered below. |
| **Recomputed from tokens** | The same lines' tokens priced per type — input, output, cache read, cache write for 5 minutes and for 1 hour, web searches — with covey's built-in price table. | The model is in the table. | The table does not know the model: its lines then count at Claude Code's own figure and are marked *Unknown model (engine price)*, without a split by type. |
| **Real cost (estimate)** | What a subscription seat's weekly meter moved, times the plan's price per point. | The credential is a subscription, its meter has been read, and a plan price is stored. If the meter did not move a whole point, the tile shows only an upper bound, *less than one point's price* (e.g. *< $0.46*), not an estimate. | The credential is an API key; no agent has woken on it yet (*no meter reading for this credential*); or no plan price (*no plan price stored*). |
| **Billed by Anthropic** | What Anthropic's Usage and Cost API reports for the API key behind the credential, after discounts and before credits. | The credential is an API key, an Anthropic admin key is stored, the credential is mapped to its API key, and the bill for the window has landed. | Any of those is missing. The page then says which — *no Anthropic admin key is stored*, *not mapped to an Anthropic API key*, *the bill sync failed*, *Anthropic's data has not landed* — and shows no figure. It never shows $0.00 for a bill that could not be read. |

Two things follow from where the first two come from:

- **Reported and recomputed come from the same lines.** The second checks the
  price table, not the tokens: a gap between them means a price differs. The
  *price-table check* beside them is that gap in percent.
- **A run that dies without a result line still spent money.** Claude Code keeps
  a session's running total, so when a later line shows the total grew by more
  than its own share and the lines in between account for, covey files the
  difference as a row of its own and books it as a cost entry of its own. It is
  only ever added, never taken away, and only where both ends are known: a
  session's first line here files none.

A booking that no line is linked to (a run cut off before its result, an engine
without result lines, a booking older than the recordings) has no tokens to
measure. It is not in the figures; the page names its amount, and in the stacked
chart it is the part *Not broken down*. Bookings that an older daemon made from
running totals (basis `legacy` or `cumulative`) are not in the figures either,
and the page counts them under *Booking check*.

## How the real cost is estimated

A Claude Code subscription has a meter: a share of a weekly allowance used so
far, in percent. covey reads it, takes how far it moved, and prices the movement.

**Meter readings.** There are two sources, told apart on the page:

- *`/usage`* — covey asks Claude Code (`claude -p /usage`) once per credential
  when an agent wakes on it, and keeps the answer for five minutes, so a fleet on
  one seat asks once. The answer is prose in whole percents. If the provider
  rate-limits the question, the engine hands back an older answer; that is
  marked stale and not used.
- *In the run's stream* — Claude Code's own `rate_limit_event`, which names every
  window the provider reports, with its utilisation and reset time. It costs
  nothing, but it is there only while a run runs.

Both read the meter of the *account*. Whatever else uses the account — a person
in claude.ai, another machine — moves it too. Readings are kept per secret value,
so reordering credentials does not break the history. Where both sources have
read a credential, the page uses the one with more readings (`/usage` on a tie)
and never adds the two: they read the same meter.

Only the weekly all-models window is priced. The other windows (five hours, a
weekly sub-limit for one model) are shown and never priced, because they count
the same work again.

**What a point is worth.** A plan's price is spread over the weeks of its period
— a month is 52/12 weeks, a year 52 — and one point of the weekly meter is a
hundredth of that. At $200 a month:

```
200 × 12 / 52 = $46.15 a week  →  $0.46 per point
```

Each reading is priced at the plan that was valid when it was read, so a price
change applies from its start and not back in time. A point costs that much only
if the week's allowance is used up; at half the allowance a used point is worth
twice as much. That is why the figure is an estimate.

**The movement.** Within a week the meter only rises, so a reading that lags
adds nothing. The first reading the calculation sees for a credential is its baseline
and counts 0; the movement is counted from the last reading before the window
(up to eight days back), or from the first one inside it when there is none —
then whatever moved before that is not in the figure. Stale and unsupported
readings are left out.

**The weekly reset.** A reading begins a new week when the reset time it names
moved forward; where it names none, when the previous reading's reset has
passed; and only where no reset time says anything, when the meter dropped by
more than one point. The first reading of a new week counts in full — the meter
started at 0 — and a reading that belongs to a week already over is dropped.
A window that spans a reset is therefore a sum of weeks.

**Whole percents, so a range.** The meter shows whole percents: *n* points of
movement are somewhere between *n* − 1 and *n* + 1, and every week in the window
leaves its own ±1 point. The tile shows the estimate with its range, for
example 12 points at $0.46 is $5.54, range $5.08 – $6.00.

**Hourly resolution.** With hourly buckets a bar shows the points read in that
hour since the previous reading. covey reads the meter when an agent wakes on
the credential or while a run streams, so:

- an hour without a reading has no movement and shows **0** — that is "not read",
  not "nothing was used";
- work from earlier hours shows up in the hour of the next reading, not in the
  hour it was done;
- an hour is only a few points, each of them ±1.

**The list-versus-real multiple.** *List $ per real $* divides the recomputed
list price of the credential's lines between the meter's baseline and its last
reading by the real cost of the same span. Only over a range is it meaningful:

- It is shown only from **3 points** of movement, with its range. At 1 or 2
  points the page says how few points the meter moved, and shows the range only.
  At 0 points it says the meter did not move a whole point, and shows no range.
- A single hour rarely reaches 3 points, and its points are not the hour's work
  (see above). A period in the chart gets a multiple by the tile's rule — from
  3 points with a plan price for each, at least one cent of list price, and not
  where the figure would read under 0.05× — and its range is in the period's
  details.
- It is not a bound in either direction. Other use of the account moves the
  meter too, which makes the true multiple higher; an allowance that is not used
  up makes each point dearer than its price here, which makes it lower.
- It needs the whole credential: with an agent filter, or in the *Per agent*
  layout, there is no multiple, because the meter does not know agents. The
  table shows each selected agent's *share of real* instead: its share of the
  recomputed list price, applied to the credential's real cost.
- Every point needs a plan price. Where some have none, the page says *no plan
  price for N of M points* and shows neither a real cost nor a multiple.

## The bill, and reconciling with it

Billed money is read, never booked: covey's cost entries stay what the daemon
reported, and the bill is stored beside them.

**Setting it up.** Three steps; only an organisation admin or the security role
may do them.

1. Store an Anthropic Admin API key (`sk-ant-admin…`) or a `read:analytics` key
   under **Secrets** as `anthropic_admin_key`. The key is the organisation's
   only; it never reaches an agent and no endpoint returns it, and covey refuses
   an `sk-ant-admin…` value stored under any other name. Saving checks the key
   against Anthropic's analytics API, and says so if an API key or a subscription
   token was filed here by mistake.
2. Say which API key each metered credential is, with
   `PATCH /api/v1/runtime-instances/{id}/credentials/{ord}/billing-key` and
   `{"api_key_id": "apikey_01…"}` (optionally `"workspace_id": "wrkspc_01…"`).
   There is no screen for this yet. `GET /api/v1/cost/billing-keys` lists the
   keys the bill names over the last 30 days with their amounts, to pick from.
   The mapping follows the secret value, from when it was written; replacing
   the value closes it. A subscription seat has no API key and is refused, and
   one API key belongs to one credential at a time.
3. Wait. covey reads the bill once an hour — the last two days in hourly buckets,
   and once a day 35 days in daily buckets, which picks up what Anthropic
   revised. The first read comes a few minutes after the server starts.
   `POST /api/v1/cost/billing-sync` reads now (`?full=1` adds the 35-day
   re-read). A read that fails keeps the rows of the last one that worked and
   says why.

**What the page shows.** The bill lands 4 to 24 hours late, so it is complete
only up to a point the page names (*complete to …, the rest pending*). The tile
sums the bill over whole billed buckets — hours within the last two days, UTC
days before — and sets the reported and recomputed figures of exactly that span
beside it, cut where the bill stops being complete. A bill over one span is
never compared with lines over another. *Billed / list* is the bill after
discounts over Anthropic's own list price of the same usage.

**Reconciliation.** `GET /api/v1/cost/reconcile?days=<n>` compares, per UTC day,
mapped credential and model, what Claude Code reported, what covey's table makes
of the same tokens (*list*), and what Anthropic billed for the key — with token
counts per type on both sides (`diff` is billed minus reported). The *residual*
is Anthropic's list price minus covey's: what Anthropic priced that covey's
tokens do not explain. Its label says how to read it:

| `residual_label` | Meaning |
|---|---|
| `match` | Under one cent apart. |
| `billed_above_list` | Anthropic priced more than covey's tokens explain: other use of the same key outside covey, tokens the lines missed, or a price the table does not know. |
| `billed_below_list` | covey's tokens price higher than Anthropic's own list figure for the key. The token diffs say which type differs. |
| `incomplete` | The day has not ended inside the data Anthropic has landed; its figures can still grow. |
| `not_comparable` | A line or a billed row ran outside standard tier, standard speed or global geography, which list prices do not cover. |
| `no_billed_data` | The day is complete and the bill has no row for this credential and model. |

A *discount* is Anthropic's list price minus its amount, so it is not a gap.
`runs_without_result` counts runs booked that day without any result line: their
spend is in the bill and nowhere on covey's side. A model the table does not
know is flagged *unpriced*, and then covey's list covers only what it knows.

## Filters

- **Time range and buckets.** 24 h, 7, 30 or 90 days, with a bucket of an hour,
  a day, a week or a month. Choosing a range picks a bucket (24 h by the hour,
  90 days by the week). Hours are offered only up to 7 days: 90 days by the hour
  would be 2,160 periods.
- **Agents.** Chips, any combination; *All* means nobody is narrowed out. Click
  adds or removes one, double-click selects only that one. Agents are grouped by
  department: the departments are ordered by their cost in the window, the
  agents without one come last under *No department*, and a department's heading
  selects or clears the whole group. A group shows its first five chips and folds
  the rest under *+N more*; a selected chip is never hidden. The department is
  the agent's department *now*, not when the cost was booked — covey keeps no
  history of moves. Where nobody has a department, the filter is one flat row.
- **Credentials.** Not a chip: the chart has one block per credential, with its
  own tiles, and a block for runs that no credential could be attributed to. The
  API narrows the per-model table to one credential with `?secret_key=` on
  `GET /api/v1/cost/models`.
- **Source.** Four chips, one per figure. *Reported* and *Recomputed* are on by
  default, *Real cost* and *Billed* are off. *Real cost* and *Billed* belong to a
  whole credential, not to an agent: under an agent filter they appear only as
  the estimated share in the table.
- **Chart.** Split by cost type or not at all; the cost types to draw; combined
  or *Per agent*; amounts or *Real vs list*, which draws only for a subscription
  credential with readings, real cost switched on, no agent filter and a plan
  price for every point.

## Plan price

On a subscription credential's block, *Set plan price* opens a form with the
amount in US dollars, the period (per month or per year) and *Valid from*. Only
an organisation admin or the security role sees it as editable.

- *Valid from* starts at the beginning of the current meter week (the latest
  weekly reset the readings name, minus seven days). When no reading names a
  weekly reset, it starts now. If a price already starts in this week, the form
  corrects it and starts now, to the minute.
- A price is added, never overwritten. The plan valid at *Valid from* ends
  there; one that starts later bounds the new one. A second price at the same
  instant is refused, and the form says to pick a later start.
- It prices the meter's points from then on, and only that: nothing steers by
  it, and it is not a budget.
- The API (`PUT /api/v1/runtime-instances/{id}/credentials/{ord}/plan`) also
  takes a currency with its rate to US dollars; the form is US dollars only.
  A plan in another currency without a rate prices nothing.
- The price follows the secret value, but the form addresses the place the
  credential sits at now; if the credentials were reordered while it was open,
  the form says so.

## Not yet covered

- **List versus real is per credential.** There is no organisation-wide real cost
  or multiple: two credentials can be seats of one account, and two sources read
  the same meter, so nothing is added across credentials or sources.
- **Operator sessions outside covey.** Claude Code sessions that drive covey from
  outside — an operator's own — are not in any figure, on any source. On a shared
  subscription they still move the account's meter, so the real cost shown is
  what the whole account used, not what covey's agents did. Tracked as issue
  #120.
- **The mapping of keys to credentials and the reconciliation have no screen.**
  Both are API only; the page shows the bill's result in the *Billed* tile.
- **Readings for Claude Code only.** Other engines have no meter reading, so no
  real cost.

## Next

- [Runtimes and engines](runtimes.md) — credentials, seats and what each engine reports
- [Identity & secrets](identity-and-secrets.md) — where a credential or the admin key is stored
- [Guard-rails](guard-rails.md) — the per-agent budget, which is a different figure from these
