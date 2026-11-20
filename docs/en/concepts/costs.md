---
slug: costs
title: Cost & Tokens
description: 'What the cost page leads with and how to read it: all the work at token prices, the one real cost of the subscription, the one multiple, and the Claude Code sessions outside covey that are counted by default. How the real cost is estimated from the provider''s meter, how the bill of an API key is read and reconciled, and what the page does not count yet.'
faq:
  - q: Which figure leads the page, and why are there other dollar figures?
    a: 'One list-price figure leads: all the work in the period at token prices, as reported by Claude Code. The others answer different questions. Real cost (estimate) is what a subscription seat used of its weekly allowance, Billed by Anthropic is what an API key was charged, and covey''s own price table shows up as a difference line under the lead figure, as its own figure in the tiles and the chart behind Compare with covey''s price table, and as the Recomputed column of the per-model table, which is on by default. The credential blocks never add up across credentials; the headline adds the subscription meters, and the per-model table has rows that sum all subscription seats and all API keys (see *Filters*).'
  - q: Does the page count my own Claude Code sessions outside covey?
    a: 'Yes, once they are pushed with covey usage push. They are included by default in every figure, always labelled as outside covey, and the covey only chip leaves them out of the tiles, the chart and the tables. The headline still shows the whole dent. See *Sessions outside covey*.'
  - q: What does the multiple mean?
    a: 'All the work on the subscription meters at token prices (covey''s agents plus the sessions outside covey, added) divided by the one real cost of those meters over the same period. The headline also gives a floor, the multiple at the low end of the range the meter''s whole percents leave. Where a meter may carry use that nobody reports, the multiple is a floor, not an estimate.'
  - q: Why does an hour show 0 real cost when agents were working?
    a: 'The real cost comes from the provider''s meter, and covey reads the meter only when an agent wakes on that credential or while a run streams. A bar shows the points read in that hour since the previous reading; an hour without a reading shows 0, and work from an earlier hour can appear in a later one. Over a longer range the movement adds up, to within one point per week.'
  - q: Why is there no "list per real" multiple for a single hour or for one agent?
    a: 'The meter moves in whole percents, so a few points are known only to within one point, and the meter covers the whole account, not one agent. The multiple is shown from 3 points of movement, over a range, for a whole credential.'
  - q: Where do I set what the subscription costs?
    a: 'On the cost page, on the credential''s block: Set plan price, and Set cost basis for what is the money on that credential (the seat, its usage, or both). An organisation admin or a security role can do both. A plan price prices the meter''s points read from its *Valid from* on, never before.'
---

The cost page (*Costs* in the *Admin* column of the sidebar; the page is headed *Cost & Tokens*) shows what the agents' work
consumed, and what Claude Code sessions outside covey consumed beside them.
Every role can read it; setting a plan price or a cost basis is for
organisation admins and the security role. All periods are cut in UTC: UTC days,
weeks that begin on Monday UTC.

A *credential* is the secret value a run went out on (an API key, or a
subscription token), and its kind says what money means on it. The page keeps
its dollar figures apart and says which question each answers.

## What the page leads with

Three figures carry the page. Everything else is detail under them.

**1. All the work at token prices.** Labelled *At token prices (as reported by
Claude Code)*: the work of the whole period, from every source, priced per call.

- For covey's agents it is Claude Code's own price of each call: the `costUSD`
  it writes on each result line, one line's own share at a time. Not covey's
  booking. (Codex reports tokens and no money; see [Runtimes](runtimes.md).)
- For sessions outside covey it is the same figure where the push carried it.
- Only a call with no reported cost is recomputed from its tokens with covey's
  built-in price table (input, output, cache read, cache write for 5 minutes and
  for 1 hour, web searches). The figure says so: *recomputed for sources without
  a reported cost*. A call of a model the table does not know has no list price:
  its tokens are counted as unpriced, never as $0.
- covey's own price table is only a check. Where it differs from the lead
  figure by more than 1 % and more than $0.50, a line under the figure says so
  (*covey's own price table gives … % less/more than Claude Code reported*).
  The per-model table is the exception: it has its own *Recomputed* column, on
  by default (see *Filters*).
  *Compare with covey's price table* (also `?prices=recomputed`) shows
  *Reported by Claude Code* and *Recomputed from tokens* as figures of their
  own, in the tiles, the chart and its tables. A gap between them means a
  price differs, not the tokens.

**2. One real cost.** What the subscription meters' weekly movement cost: the
points the meter moved, times the plan's price per point. It exists only for a
subscription with meter readings and a plan price (see *How the real cost is
read*). It is an estimate (*≈*), and the meter is account-wide: whatever else
uses the account moves it too.

**3. One multiple.** The work at token prices (covey's agents and the outside
sessions, added) divided by that one real cost: how many list-price dollars one
real dollar of the subscription bought. Over the readings only; list price from
before the meter's first reading or after its last one is named apart, because
it has no real cost yet. Next to it the page gives a floor (*At least …×*): the
meter is known to ±2 points per seat and 2 more per weekly reset, so the
multiple cannot be lower than the list price over the real cost plus that
slack. A multiple needs at least 3 points of movement and a plan price for each.

The top of the page shows these as one sentence in the card *The whole dent in
the subscription* once a source of outside sessions is linked to a meter:

> All this work on the subscription meters at token prices (as reported by
> Claude Code): *$X* = covey's agents *$A* + sessions outside covey *$B*,
> against the real cost of the subscription for the same period: *$R* (*N*
> points of the meter). → *$X'* ÷ *$R* ≈ *M*×.

Each subscription credential's block shows the same three for its own seat:
the lead figure, *Real cost (estimate)*, and *List $ per real $* (with outside
sessions on the seat, *— everything on this meter*). The headline ignores the agent filter and says so.

Where a source of outside sessions is linked to a meter but has not reported
through every reading, or no source is linked and the meter may carry use that
nobody reports, covey's multiple is a floor (*≥*, *at least: the meter may
include use outside covey*), not an estimate.

**How the real cost splits.** Below the headline a collapsed section, *How the
real cost splits (estimate)*, gives each party's real share: covey's agents, each
source of outside sessions, and two parts nobody owns.

- *Unattributed* is movement of the meter that neither covey's agents nor the
  reported sessions explain, for example another machine on the same account.
  It is booked to nobody, never to covey.
- *Not split* is movement in readings the outside sessions had not reported
  yet (or whose calls had no price): it is split to nobody, not booked to covey's
  agents.

The shares are estimates, and a share's multiple is a lower bound.

**The other figure.** *Billed by Anthropic* is what Anthropic's Usage and Cost API
reports for the API key behind a credential, after discounts and before credits.
It never shows $0.00 for a bill that could not be read: it says why instead
(*no Anthropic admin key is stored*, *not mapped to an Anthropic API key*, *the
bill sync failed*, *Anthropic's data has not landed*). It is never added to the
figures above; see *The bill, and reconciling with it*.

A run that dies without a result line still spent money: when a later line shows
Claude Code's running total grew by more than the lines account for, covey files
the difference as a row and a cost entry of its own (only added, never taken
away, and only where both ends are known). A booking that no line is linked to
has no tokens to measure and is not in the figures; the page names its amount
(in the chart, *Not broken down*), and bookings an older daemon made from running
totals are counted under *Booking check*.

## Sessions outside covey

Claude Code sessions that drive covey from outside (an operator's own, a work
agent's) run on the same subscription meter, or on the same per-use bill, as
covey's agents. covey does not read them itself: a machine sends their token
counts, and nothing else, with `covey usage push` (`--source-per-project`,
`--label`, `--host`, `--complete`). How to run it, what it sends and how a
claim of completeness works is in [Sessions outside
covey](../operations/usage-push.md); until a source has pushed, nothing of it
shows.

- **Included by default, always labelled.** The tiles, the chart, the agent and
  model tables and the credential blocks count the outside sessions. Each figure
  names its two parts (*covey agents $A · outside sessions $B*), and the chart
  draws each source named and hatched, marked *outside covey*. Under an agent
  filter a figure reads *selected agents plus outside sessions*: the outside
  sessions are not agents.
- **covey only.** The chip *covey only*, above the tiles, leaves them out of the
  tiles, the chart and the tables (`?only=covey`). It appears when at least one
  source has calls in the range. The headline still shows the whole dent.
- **Detailed view.** The section *Operator sessions (outside covey)* shows them
  apart or combined with the agents; *Detailed view →* opens `/costs/outside`:
  list price over time, per source, per model, main session against subagents
  and workflows, tokens per type with their dollars, and each source's share of
  the subscription meter. Every role can read it.
- **Sources on no meter.** A source on no subscription meter the page reads
  (billed per use, or linked to a seat with nothing in the range): its work is
  in the tiles and named apart in the headline, and has no real share.
- **Use nobody reports.** Meter use that no source reports is shown as
  *Unattributed* and is never booked to covey (see above).

## How the real cost is read

A Claude Code subscription has a meter: a share of a weekly allowance used so
far, in percent. covey reads it, takes how far it moved, and prices the movement.
Only the weekly all-models window is priced; the five-hour and the per-model
windows are shown and never priced, because they count the same work again.

- **Readings.** covey asks Claude Code (`claude -p /usage`) once per credential
  when an agent wakes on it (the answer is kept for five minutes), and reads
  Claude Code's own `rate_limit_event` while a run streams. Where both have read
  a credential the page uses the one with more readings and never adds them. While
  no agent runs and covey does not poll the seat (below), no reading arrives: work
  after the last reading is named apart (*after its last reading, real share
  pending until the meter is read again*). The meter belongs to the *account*, so
  another person or machine on it moves it too.
- **covey's own poll.** Where no run reads a subscription seat that outside
  work shares, covey reads it itself: one Haiku turn through the `claude` CLI on
  the seat's token, which prints the same `rate_limit_event`. Only Claude Code
  seats holding a Claude OAuth token (`sk-ant-oat…`) are polled; another
  engine's subscription is never handed to the CLI. A seat is polled only when
  an outside source is linked to it (the poll's own source does not count): a
  seat no outside source names has no outside work to split, and stays all
  covey's. Of those, a seat is polled when no supported reading of it (in band,
  polled, or from `/usage`; one entered by hand does not count) arrived in the
  last 10 minutes and either such a source pushed within the last hour or the
  organisation's fleet is paused — the fleet switch is off, or the organisation
  has hired agents and every one of them is killed (a draft agent does not
  count). Each control-plane process tries one seat at most once in 10 minutes;
  after the first try in a row that files no reading (the call failed, or
  printed no `rate_limit_event`) the wait stays at 10 minutes, every further
  miss in a row doubles it, up to 6 hours, and a try that files one starts it
  over. The readings are the same `rate_limit_event` a run streams, at the same
  precision, and are filed as in-band readings: they continue the seat's
  in-band series, the one the page already reads for a seat covey runs on. The
  call moves the meter it reads, so it is booked as outside usage of a source of
  its own, `meter poll: <secret>#<slot>` (shortened with a hash where the
  secret's name is long), linked to the seat and dated at the call's start, and
  takes its share of the step like any other session; a call whose cost the CLI
  did not report is not booked, and its reading is not filed either. Such a
  call, and one that failed after reaching the model, still moved the meter:
  its few hundred Haiku tokens go unbooked and fall to the other users of the
  step whose reading holds them. Uploads under a source beginning with `meter
  poll` are refused; a source of such a name uploaded before that, from a host
  other than covey's own, stays an outside source like any other. The poll needs the `claude`
  CLI on the control plane's machine: `COVEY_METER_POLL` is `auto` (the
  default: `claude` from `PATH`; the container image has none, so there the
  poll is off and the log says so once at start), `off` (also `false`, `no`,
  `0`), or the binary's path, checked once at start. With the poll off, the
  watermarks of poll sources booked earlier still move up every minute, so they
  never hold a seat's later readings back.
- **A point's worth.** A plan's price is spread over the weeks of its period (a
  month is 52/12 weeks, a year 52), and one point of the weekly meter is a
  hundredth of that: $200 a month is $46.15 a week, $0.46 a point. Each reading
  is priced at the plan valid when it was read. A point costs that much only if
  the week's allowance is used up, which is why the figure is an estimate.
- **Movement.** Within a week the meter only rises. The first reading of a
  credential is its baseline and counts 0; a reading whose reset time moved
  forward starts a new week and counts in full.
- **Whole percents, so a range.** *n* points are somewhere between *n* − 1 and
  *n* + 1, and every week leaves its own ±1. 12 points at $0.46 is $5.54, range
  $5.08 – $6.00.
- **Hourly resolution.** A bar shows the points read in that hour since the
  previous reading. An hour without a reading shows **0** (not read, not "nothing
  was used"), and work shows up in the hour of the next reading.

**When there is a multiple.**

- From **3 points** of movement, with a plan price for each point. At 1 or 2
  points the page says how few the meter moved and shows the range only. At 0 it
  says the meter did not move a whole point, and the real cost tile shows an upper
  bound, *less than one point's price for each meter week in the range* (e.g.
  *< $0.46* within one week).
- A period in the chart gets one by the same rule, with at least one cent of list
  price and not where the figure would read under 0.05×; a single hour rarely
  qualifies.
- A credential's own estimate is not a bound: other use of the account makes the
  true multiple higher, an allowance not used up makes it lower. Where outside
  sessions share the meter, covey's list over the whole movement would understate
  it, so the tile shows no covey-alone multiple; the headline and *everything on
  this meter* give it.
- It needs the whole credential: with an agent filter or in the *Per agent*
  layout there is none, because the meter does not know agents. The table shows
  each agent's *share of real* instead.

## Cost basis: what is the money on a credential

A seat and its usage can both cost money. The *cost basis* says which is the
money, per credential and from a date on; each period counts by the basis valid
then.

| Basis | The money is | Effect |
|---|---|---|
| **Seat price** (the default) | The plan price of the seat. | The meter's movement is priced as above. |
| **Usage** | What the seat was charged for its use. | Its meter points are out of the real cost and the multiple (*Billed by usage: the meter is not the money*). |
| **Seat price and usage** | Both. | The seat's part comes from the meter, the usage charges stand beside it. With a USD figure for the charges and a priced real cost, the block also shows *Real cost, seat + usage*: seat plus usage, in dollars, with the sub-line *seat ≈ $X + usage $Y*. A credential whose basis changed in the window shows it too. |

Usage charges are entered by hand on the credential's block (*Add usage charge*:
amount, currency, from, to, optional USD rate with its date). A charge keeps its
own currency, and a USD figure and a usage multiple appear only with an explicit
rate. The usage multiple counts covey's list only over the spans the counted
charges cover (boundary periods pro rata by time), with *≥* where sessions
outside covey share the credential. Charges stand beside the real cost, per
currency, and are never added across currencies; they are added to the seat's
real cost only in the one tile *Real cost, seat + usage*, and only in USD. They
count only for the time the credential has a usage basis. The headline still
leaves usage charges out: it lists them beside the real cost, in none of its
figures. For an API key the bill is synced and stands
beside them: a charge entered here must not be that same bill, and overlapping
charges are refused. *Set cost basis* and *Add usage charge* are for
organisation admins and the security role; every role reads the line *Charged by:
…* and the charges.

## The bill, and reconciling with it

Billed money is read, never booked: covey's cost entries stay what the daemon
reported, and the bill is stored beside them. Setting it up is for organisation
admins and the security role:

1. Store an Anthropic Admin API key (`sk-ant-admin…`) or a `read:analytics` key
   under **Secrets** as `anthropic_admin_key`. It never reaches an agent, and
   covey refuses an `sk-ant-admin…` value under any other name. Saving checks it
   against Anthropic's analytics API.
2. Say which API key each metered credential is, with
   `PATCH /api/v1/runtime-instances/{id}/credentials/{ord}/billing-key` and
   `{"api_key_id": "apikey_01…"}` (optionally `"workspace_id": "wrkspc_01…"`).
   There is no screen for this. `GET /api/v1/cost/billing-keys` lists the keys the
   bill names over the last 30 days. The mapping follows the secret value; a
   subscription seat has no API key and is refused, and one API key belongs to
   one credential at a time.
3. Wait. covey reads the bill once an hour (the last two days by the hour) and
   once a day (35 days by the day, which picks up what Anthropic revised).
   `POST /api/v1/cost/billing-sync` reads now (`?full=1` adds the 35-day re-read).
   A failed read keeps the last good rows and says why.

The bill lands 4 to 24 hours late, so it is complete only up to a point the page
names (*complete to …, the rest pending*). The tile sums whole billed buckets and
sets the reported and recomputed figures of exactly that span beside it.
*Billed / list* is the bill after discounts over Anthropic's own list price of
the same usage.

`GET /api/v1/cost/reconcile?days=<n>` compares, per UTC day, mapped credential
and model, what Claude Code reported, what covey's table makes of the same tokens
(*list*) and what Anthropic billed, with token counts per type (`diff` is billed
minus reported). The *residual* is Anthropic's list price minus covey's, and
`residual_label` says how to read it: `match` (under one cent apart),
`billed_above_list` (other use of the same key, missed tokens, or a price the
table does not know), `billed_below_list` (the token diffs say which type),
`incomplete` (the day is not over in the landed data), `not_comparable` (a line
or row outside standard tier, speed or geography) and `no_billed_data`. A
*discount* is not a gap. `runs_without_result` counts runs booked without a
result line: their spend is in the bill and nowhere on covey's side.

## Filters

- **Time range and buckets.** 24 h, 2, 7, 30 or 90 days, with a bucket of 10
  minutes, an hour, 10 hours, a day, a week or a month. Choosing a range picks a
  bucket (24 h and 2 days by the hour, 7 and 30 days by the day, 90 days by the
  week). A fine bucket is offered only up to a limit: 10 minutes up to 2 days,
  hours up to 7 days, 10 hours up to 30 days; 90 days by the hour would be 2,160
  periods. The meter and the bill are read per hour, so at 10 minutes *Real cost*
  and *Billed* are off and the page says why.
- **Agents.** Chips, any combination; *All* means nobody is narrowed out. Click
  adds or removes one, double-click selects only that one. Agents are grouped by
  department: the departments are ordered by their cost in the window, the
  agents without one come last under *No department*, and a department's heading
  selects or clears the whole group. A group shows its first five chips and folds
  the rest under *+N more*; a selected chip is never hidden. The department is
  the agent's department *now*, not when the cost was booked — covey keeps no
  history of moves. Where nobody has a department, the filter is one flat row.
  The outside sessions stay in under a filter, as *plus outside sessions*, unless
  *covey only* is on.
- **Credentials.** Not a chip: the chart has one block per credential, with its
  own tiles, and a block for runs that no credential could be attributed to. The
  API narrows the per-model table to one credential with `?secret_key=` on
  `GET /api/v1/cost/models`.
- **Source.** In the chart, one chip per figure: the lead figure (*At token
  prices (as reported by Claude Code)*), *Real cost* and *Billed*, all on by
  default, and one chip per source of outside sessions. *Compare with covey's
  price table* adds *Recomputed* as a chip of its own. *Real cost* and *Billed*
  belong to a whole credential, not to an agent: under an agent filter they
  appear only as the estimated share in the table. The per-model table has its
  own source switches: *Reported* and *Recomputed* are on by default, *Real cost*
  and *Billed* are off. With either of those two on, the table also gets one row
  per source under its total:
  - *Subscription seats, meter span {from} – {to}* sums the real cost of every
    seat: about the sum (*≈*), with the summed range the whole percents allow.
    If every seat sits at 0 points, it shows only an upper bound, *<* the summed
    upper bounds.
  - *API keys, bill {from} – {to}* sums every measured bill.
  - Either row shows *–* instead of a figure when one of its credentials has
    none; hovering says how many (*{n} of {m} credentials without a figure*).
  - The rows cover the whole organisation even under an agent filter and are
    marked *whole organization: does not follow the agent filter*; they do not
    show the agent's share.
- **Chart.** Split by cost type or not at all (with *Compare with covey's price
  table*); the cost types to draw; combined or *Per agent*; amounts or *Real vs
  list*, which draws only for a subscription credential with readings, real cost
  switched on, no agent filter and a plan price for every point.

## Plan price

On a subscription credential's block, *Set plan price* opens a form with the
amount in US dollars, the period (per month or per year) and *Valid from*. Only
an organisation admin or the security role sees it as editable.

- *Valid from* starts at the beginning of the current meter week (the latest
  weekly reset the readings name, minus seven days, moved on to the week that
  contains now), or now when no reading names a reset. If a price already starts
  in this week, the form corrects it and starts now, to the minute.
- A price is added, never overwritten: the plan valid at *Valid from* ends there,
  one that starts later bounds the new one, and a second price at the same instant
  is refused.
- It prices the meter's points from then on, and only that: nothing steers by it,
  and it is not a budget.
- The API (`PUT /api/v1/runtime-instances/{id}/credentials/{ord}/plan`) also takes
  a currency with its rate to US dollars; the form is US dollars only. A plan in
  another currency without a rate prices nothing.

## Not yet covered

- **The multiple adds seats.** The headline's real cost adds the meters of every
  subscription seat it can read, one line per meter in the collapsed split, and
  the *Subscription seats* row of the per-model table (see *Filters*) sums
  seats too: two seats of one account are each counted in full there. The
  credential blocks never add across credentials.
- **Outside sessions arrive by push.** covey reads none itself. A source that
  has not reported through a meter reading leaves that reading unsplit, and a
  paused fleet reads no meter itself, so where covey's own poll does not read
  the seat (no `claude` CLI, or no outside source linked) its movement waits
  until the next reading.
- **The mapping of keys to credentials and the reconciliation have no screen.**
  Both are API only; the page shows the bill's result in the *Billed* tile.
- **Readings for Claude Code only.** Other engines have no meter reading, so no
  real cost.

## Next

- [Sessions outside covey](../operations/usage-push.md) — `covey usage push`, one source per project, labels and the claim of completeness
- [Runtimes and engines](runtimes.md) — credentials, seats and what each engine reports
- [Identity & secrets](identity-and-secrets.md) — where a credential or the admin key is stored
- [Guard-rails](guard-rails.md) — the per-agent budget, which is a different figure from these
