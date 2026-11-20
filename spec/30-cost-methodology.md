# 30 — Cost methodology (how covey computes money)

This is the reference for every money figure covey shows: what it means, how it is computed, where the code is, and how to check it with a query. It is written so that someone in finance can follow it without reading Go. The cost page's user guide is [`docs/en/concepts/costs.md`](../docs/en/concepts/costs.md), and the way runs are booked is in [`06-observability-control.md`](06-observability-control.md#cost-control). This document gives the rules behind both.

Every figure must have three things:
- **A formula** that can be said in one sentence.
- **A named source**: what Claude Code reported, what covey computed, what the provider metered, or what the provider billed.
- **A test** that pins it with hand-computed values.

A figure that lacks one of them is a defect, not a choice of presentation.

State checked: `main` at `b978e2a` (2026-11-20). Where the code breaks a rule, the rule says so and names the issue.

## Words

| Word | Meaning | Where it lives |
|---|---|---|
| **Credential** | The secret value a run went out on: a subscription token (a *seat*) or an API key. Every cost row is keyed on it (`secret_key`, `secret_slot`), not on the runtime, so its history survives a reorder. | `model_usage`, `usage_readings`, `credential_*` tables |
| **Reported price** | The dollar figure Claude Code writes on each result line (`costUSD`), one line's own share at a time. | `model_usage.engine_usd` (0142/0144); `external_usage.reported_usd` (0150) |
| **List price** (recomputed) | The tokens of a call priced with covey's table: input, output, cache read, cache write 5 min, cache write 1 h, web searches at $0.01. Thinking tokens are output. | `ListPrice`, `internal/observability/listprice.go:30` |
| **Lead price** | The figure the page leads with: per call, the reported price where there is one, else the list price. A call with neither is *unpriced*, never $0. | the comment "The lead price (#120 …)", `internal/observability/external.go:667`. covey's lines always carry Claude Code's figure (`model_usage.engine_usd`), so their lead price is it; an outside call carries one only sometimes (`reported_usd`). |
| **Booked cost** | What covey filed per run in `cost_entries` for budgets and guard rails. An accounting record, not a price. | `cost_entries` (0144, 0147) |
| **Meter** | The provider's own usage gauge for a subscription seat: percent of the weekly allowance used, in whole percents, account-wide. | `usage_readings` (0142/0145) |
| **Plan price** | What a seat costs per month or year, in its currency, with its exchange rate, valid from a date. | `credential_plans` (0145) |
| **Bill** | What Anthropic charged an API key, read from its Usage and Cost API. | `billed_cost`, `billed_usage` (0146) |
| **Cost basis** | What the money on a credential is: its seat price, its usage, or both. Dated. | `credential_cost_bases` (0151) |
| **Usage charge** | Money charged for use over a span, entered by hand (an invoice), or derived from a monthly usage limit. | `credential_usage_charges` (0151), `credential_usage_limits` (0152) |
| **Point** | One percent of a seat's weekly allowance. | |
| **Step** | One meter reading and the span back to the reading before it: (t<sub>i−1</sub>, t<sub>i</sub>]. | `ExternalMeterStep` |
| **Chunk** | Consecutive steps of one seat that together moved the meter at least 5 points. Covey splits the real cost per chunk. | `meterChunk`, `external.go` |

## The rules

### R1 — There is one real cost

**Rule.** For each credential, exactly one figure is *real*. It is what the organisation pays:
- **A flat seat (subscription).** Real cost = the points the weekly meter moved, times the plan's price per point. The movement is counted only inside one reset period at a time.
- **Pay-per-token.** Real cost = the bill. Until the bill has landed, it is the change in the monthly meter's percent times the monthly limit in force, in the limit's currency.

Token prices are never real cost. That covers the reported price, the recomputed list price and the booked cost. They measure how much work was done, and they are compared with the real cost. They are never a substitute for it.

**How covey computes it.**
- **Price per point.** Weekly price = amount × fx ÷ 52 for a yearly plan, or amount × fx × 12 ÷ 52 for a monthly plan. A point is a hundredth of that. At $200 a month, a week is $46.15 and a point is $0.4615. Code: `WeeklyUSD`, `internal/observability/stacked.go:566`; on the web, `pricePerPoint`, `web/src/pages/costFigures.ts:131`.
  - A point is priced at the plan valid when it was read (`planAt`, `stacked.go:606`).
  - A point no plan prices stays *unpriced*. It is never $0.
  - A point read while the basis was *usage* belongs to the usage charges, not the plan.
- **Points.** Code: `meterQueryTemplate`, `stacked.go:643`. Readings are taken per credential, reading source and window.
  - Each source's series is counted on its own (`meterSteps`, `stacked.go:743`). A credential's real cost, however, is read from **one** source only: its `seven_day` series with the most readings, with `usage_cmd` before `manual` before `inband` on a tie (`pickCoverage`, `external.go:1324`; on the web `pickMeter`, `costFigures.ts:222`). Two sources read the same meter, so their points are never added.
  - Stale and unsupported readings are dropped.
  - A late reading is dropped: one whose reset time lies more than an hour before the latest reset time any earlier reading gave. A run of late readings is dropped as a whole.
  - A new week starts:
    - where a reading and the one before it both carry a reset time, when the reset time moved forward by more than an hour (a manual reading may differ from the provider's last reset by up to an hour, `internal/httpapi/costinsights.go:306`);
    - where only the reading before carries one, when that reset time has passed (not while it is still ahead);
    - where the reading before carries none, when the meter dropped by more than one point. A drop of one is a lagging reading.
  - Within a week a step adds the rise of the running maximum, so a lagging reading adds 0.
  - The first reading of a new week counts in full, because the meter started at 0. The first reading of a credential ever is the baseline and counts 0.
  - Only the `seven_day` all-models window is priced. The 5-hour and per-model windows count the same work again.
- **Seat range.** The meter shows whole percents, so each stretch of one week is known only to ±1 point (`meterPartition`, `stacked.go:821`). Example: 12 points at $0.4615 is $5.54, with a range of $5.08 to $6.00.
- **Monthly limit (pay-per-token seat).** Code: `meterLimitCharges`, `internal/observability/limitcharges.go:236`. Spend at a reading = utilisation × the limit valid at that reading. A step is the rise in spend within the month. So a limit changed mid-month is priced as Δ(u × L), not Δu × L (H16). The money stays in the limit's currency. Its range is ±1 point of the limit per month segment.
  - Unlike the weekly meter, all sources of a credential and window are merged into one series (`monthlySeries`, `limitcharges.go:125`).
  - A reading with no limit in force names no money. Where the reading before had no limit (a limit entered mid-month), its spend is read at this reading's limit, so the month's spend before the limit is not this limit's step.
  - A step's span starts no earlier than the limit row's `valid_from` (a step across a limit change, or from a reading without a limit, belongs to the new row) and no earlier than the month's start (a new month's money lies after the reset). The part of a span before the range is cut off pro rata by time.
  - The money counts only for the share of the span a usage basis held (`usageShare`), as a hand-entered charge does.
- **The bill (API key).** `billed_cost.amount_cents` is after discounts and before credits, and is exact (NUMERIC from the API's decimal strings).
  - A reader takes one bucket width per UTC day, never both (`chosenRows`, `internal/observability/billing.go:381`).
  - The bill is complete only up to `data_refreshed_at`. It lands 4 to 24 hours late.
  - An empty answer with no refresh time has *not landed*. It is not zero.

**Status.**
- *Holds* for seats, and for the bill once it has landed.
- **Not built for pay-per-token before the bill lands: [#167](https://github.com/jmarcone/covey/issues/167).** The code has no fallback to the monthly meter. Until the bill lands, *Billed* says *not landed* (`billedFigure`, `costFigures.ts:438`), not Δ(u × L). The monthly-limit meter becomes money only as usage charges, in their own tile. The page has no headline or multiple for a pay-per-token credential yet.
- Readings arrive only while covey's agents run, so a paused fleet reads no meter ([#153](https://github.com/jmarcone/covey/issues/153)).

### R2 — The multiple, over metered windows only

**Rule.** The multiple = Σ token prices of all sources ÷ the real cost, over the same time.
- "All sources" means covey's agents plus the sessions outside covey that share the meter.
- "The same time" means only the spans between two meter readings:
  - never across a reset;
  - never with usage from before the first reading or after the last one.
- The multiple is a **floor**. Use the meter carries that nobody reports (another machine on the account) raises the real cost without adding to the numerator, so the true multiple can only be higher.
- Booked cost ÷ real cost is never a valid multiple.

**How covey computes it.**
- **Steps.** Each seat's steps span (t<sub>i−1</sub>, t<sub>i</sub>] (`seatSteps`, `external.go:1403`). The step's lists are the lead price of covey's lines (`model_usage.created_at` in the span) and of the linked outside calls (`external_usage.ts` in the span).
- **Metered list.** Only the priced steps count. List that lies before the first reading or after the last one is named apart as *unmetered* (`unmeteredList`, `external.go:1792`). The page states it on its own line ("before its first reading" / "after its last reading, pending"). The breakdown is range = metered + before + after + unpriced + usage (`seatLists`, `costFigures.ts:965`).
- **Estimate and floor.**
  - Estimate = metered list ÷ R.
  - Floor = floor list ÷ (R + slack).
  - The floor list leaves out lines that began before the range, and outside calls from before the range in the first step's span (`floorCovExcludedBy`, `external.go:1730`).
  - Slack = 2 points × (1 + number of resets) per seat (`meterSlack`, `costFigures.ts:913`).
    - Steps read under the usage basis are left out of the resets counted.
    - A seat whose every step was read under the usage basis has no slack.
    - Otherwise a seat gets slack when it has points, a reset, or list in the range. "List in the range" is either of two tests: list on one of its steps not read under the usage basis, or list anywhere in the seat's range (`seatHasList`, `costFigures.ts:1019`: covey's or the outside range list above 0). That range list includes list before the first reading, after the last one, at unpriced steps and at usage-basis steps (`seatLists`). So a seat whose only list lies outside its readings still gets 2 points of slack.
    - A seat with none of these (no points, no reset, no list in the range) has no slack.
    - A point is priced at the dearest plan price the server gave a step of that seat. Without one, it uses the plan valid now (else the seat's latest plan), else the whole meter's dearest price (`slackRate`, `costFigures.ts:896`).
  - Code: `headline` (`costFigures.ts:1595`) and `everythingOnMeter` (`costFigures.ts:1025`).
- **When there is no multiple.** The page says which of these applies, checked in this order:
  - *usage*: every reading was taken under the usage basis, so the meter is not the money. For the headline, this applies when no seat has a priced reading and none is unpriced.
  - *unpriced*: any point has no plan price. For the headline, this also applies when no seat has a priced reading at all.
  - *movement*: fewer than 3 points. With plan prices that differ between seats (`point_rate_mixed`), the headline cannot add points. It then gates on real ÷ the dearest price per point < 3 instead.
  - *list*: less than $0.01 of metered list, or a real cost that is not above 0.
  - Code: `headline` (`costFigures.ts:1595`) for the page's headline, and `everythingOnMeter` (`costFigures.ts:1025`) for one seat.
- **covey's list alone.** Where outside sessions share a seat, covey's list alone over the whole movement would understate the multiple. The seat then shows only the multiple of everything on the meter. With no source linked, covey's own multiple is shown as a floor ("at least").

**Status.**
- **Breaks the rule at a reset: [#185](https://github.com/jmarcone/covey/issues/185).**
  - A step whose span contains a weekly reset counts only the new week's points. Its list, however, still covers the old week's tail.
  - k already leaves that step out. The metered list, the floor and the split do not.
  - Example ($200/month plan): $50 of old-week work and $25 of new-week work against 7 new-week points.
    - Shown: estimate 23.2×, floor 14.8×.
    - True multiple of all that work: 9.6×.
    - Correct new-week floor: 4.9×.
    - So the shown floor is not a floor.
- The rest holds.

### R3 — Attribution of the one real cost is an estimate

**Rule.** The real cost of a seat is split between covey's agents, each source of outside sessions, and *unattributed*. The split is by token-price share inside each metered chunk of at least 5 points.
- It is an estimate, and its uncertainty is stated with it: ±2 points per seat plus 2 per reset for the whole.
- A party's part is never shown without its range.

**How covey computes it (F3c).** Code: `seatChunks`, `external.go:2304`; `chunkRates`, `external.go:2462`; `chunkUnattributed`, `external.go:2519`; `splitSeatSince`, `external.go:2700`.
1. **Build the chunks.** Walk a seat's steps in time order. A step joins the open chunk, and the chunk closes at the reading where its points reach 5 (`chunkMinPoints`, `external.go:2139`).
   - Some steps close the chunk before themselves and stay out of every chunk:
     - a step that is not yet reported;
     - a step whose outside calls are *all* unpriced (`outside_unpriced`, `stepReason`, `external.go:2112`);
     - an unpriced step;
     - a step read under the usage basis.
   - A reset closes the chunk, and so does a change of plan price.
   - So the last chunk, and a chunk closed early, can hold fewer than 5 points. A meter lag of up to 1 point right after a full chunk is folded into it.
   - A step where only *some* outside calls are unpriced stays split on the priced ones and joins the chunk. Its unpriced calls make the chunk *incomplete* (`meterChunk.incomplete`, `external.go:2199`). So do covey bookings that have no line.
2. **Per chunk.** P<sub>c</sub> is the points, L<sub>c</sub> the list, Lo<sub>c</sub> and Lc<sub>c</sub> the outside and covey parts of it, and S<sub>c</sub> = 2 points of slack (whole percents at both ends, one point of lag).
3. **The rate k.** For each plan price, k = the lowest (P + S<sub>c</sub>) ÷ L over chunks, or stretches of adjacent chunks, that meet all of these:
   - *Adjacent* means each chunk was opened right after the one before closed at its size. Nothing (a reset, an unsplit step, a change of plan price) closed it early. So a stretch has one plan price.
   - No chunk in it is incomplete. That means no unpriced outside call, and no covey booking without a line. The stretch ends at the first incomplete chunk.
   - P is at least 10 points, and L is above 0.
   - Reset steps are left out of P and L.
   - A covey line spread over several spans counts at its whole list when it began inside the stretch (not in a reset step). Otherwise it does not count.
4. **Unattributed points.**
   - With k known: U<sub>c</sub> = max(0, P<sub>c</sub> − S<sub>c</sub> − k·L<sub>c</sub>). Here L<sub>c</sub> counts every spread line that ran in the chunk at its whole list (`chunkListHigh`, `external.go:2548`), so U<sub>c</sub> is at most the true unattributed points.
   - A chunk with no list at all: U<sub>c</sub> = P<sub>c</sub>, because the meter moved and nobody reported a call (`noCall`, `external.go:2212`). Two exceptions:
     - No list, but covey bookings without a line that carry a booked cost, and no unpriced outside call: the chunk is incomplete, U<sub>c</sub> = 0, and its share is 0, so the whole movement is covey's (`split`, `external.go:2204`).
     - No list, covey bookings without a line, *and* unpriced outside calls: whose use moved the meter is unknown. The chunk is not split at all (`unknown`, `external.go:2208`; see R4 *Not split*).
   - No k, or the chunk is incomplete (covey bookings without a line, or unpriced outside calls): U<sub>c</sub> = 0. The chunk's whole movement is then split by the priced list share in step 5, including movement nobody's calls explain. See R4 and [#196](https://github.com/jmarcone/covey/issues/196).
5. **Split each step of a chunk.**
   - R<sub>un</sub> = R × U<sub>c</sub> ÷ P<sub>c</sub>.
   - R<sub>out</sub> = (R − R<sub>un</sub>) × Lo<sub>c</sub> ÷ L<sub>c</sub>.
   - R<sub>cov</sub> is the rest.
   - covey's part goes to its agents by their share of Lc<sub>c</sub>. The outside part goes to the sources by their share of Lo<sub>c</sub>.
6. **Ranges.**
   - The whole meter: the floor's slack of R2, 2 points per seat plus 2 per reset.
   - Each part (`SplitExternalSlack*`, `SplitCoveySlack*`, summed in `splitSeatSince`), one term per split chunk with a step in the range. With s the chunk's outside share (1 − s for covey):
     - S<sub>c</sub> = 2 points × the part's share. In dollars, that is 2 × ½·TV of the chunk's steps' plan prices (`rateTV`, `external.go:2605`).
     - A chunk that began before the range adds its attributed points outside the range, *extra* = max(0, (P<sub>c</sub> − U<sub>c</sub>) − (P<sub>in</sub> − U<sub>in</sub>)), times the part's share (`sinceCuts`, `external.go:2651`). Without it, a part's figure for the range would not bound its true use.
     - The share's own range. A spread line's list is placed by time, which may put it in the wrong chunk, so the true outside share lies in [s<sub>lo</sub>, s<sub>hi</sub>] (`chunkShareRange`, `external.go:2575`). With b = the chunk's attributed points in the range + *extra* + S<sub>c</sub>, the outside part adds b × (s<sub>hi</sub> − s) and covey's part adds b × (s − s<sub>lo</sub>).
   - A part's multiple is shown only as a floor, list ÷ (part + its slack) (`subscriptionDent`, `costFigures.ts:1676`).

**Status.** *Holds*, with one difference from the plain rule and two defects.
- **The difference.** A part's range is 2 points *per chunk*, which is wider than "±2 plus 2 per reset" as soon as there are several chunks. That is deliberate and safe: the share changes from chunk to chunk, so one range for the whole would not bound a part.
- **The inherited defect.** Through #185, a reset step's old-week list takes part in its chunk's share.
- **Incomplete chunks:** their parts can hold movement nobody's calls explain ([#196](https://github.com/jmarcone/covey/issues/196), under R4).
- The remaining edge cases are listed in [#158](https://github.com/jmarcone/covey/issues/158).

### R4 — Parts sum to the total, and the rest stays visible

**Rule.** Every split adds up to the figure it splits. What nobody can be given stays visible as a part of its own and is never folded into covey.

**How covey computes it.** Each identity below is pinned by a test.
- **Every step:** covey + outside + unattributed + unsplit = R. Test: `internal/observability/external_test.go:498`.
- **Agents:** covey's agents add up to covey's part (test at `external_test.go:324`).
- **Sources:** the sources add up to the outside part (`SourceParts`).
- **Seat sums:** `RealUSD` = `CoveyRealUSD` + `ExternalRealUSD` + `UnattributedRealUSD` + `UnsplitRealUSD` (`ExternalMeterSums`, `external.go`).
- **Lists:** range = metered + before + after + unpriced + usage, for each party (`seatLists`).

What each of the rest means:
- *Unattributed*: movement nobody's calls explain, beyond what the rate k accounts for (R3 step 4). In a complete chunk it goes to nobody. An incomplete chunk has none; see below.
- *Not split*: movement in steps of three kinds:
  - steps the outside sessions had not reported yet;
  - steps whose outside calls were *all* unpriced (`outside_unpriced`);
  - steps of a chunk with no list that holds both covey bookings without a line and unpriced outside calls (`unknown()`, `external.go:2809`). These are counted with the `outside_unpriced` readings (`OutsideUnpricedRealUSD`), never covey's.
- *Unpriced*: points with no plan price, or tokens of a model the table does not know. They are counted and named, never $0.
- *Before / after the readings*: list with no metered real cost.

The page shows unattributed and not split in the collapsed section *How the real cost splits (estimate)*.

**Incomplete chunks: what the code does.** A chunk is incomplete when it holds outside calls with no price next to priced ones, or covey bookings without a line (`meterChunk.incomplete`, `external.go:2199`).
- It has no unattributed part: U<sub>c</sub> = 0 (`chunkUnattributed`, `external.go:2519`).
- Its whole movement is split by the *priced* list share. That includes the unpriced calls' movement and any movement nobody's calls explain. So covey's part takes covey's list share of both. Covey bookings without a line work the other way: covey's unlisted use goes partly to the outside part.
- The only signal is the count `IncompleteChunks` (`external.go:750`). It is counted per split chunk in the range: a chunk is counted when it is split and its last step is not before the range's start (`external.go:2907`). Incomplete chunks wholly before the range, and the not-split chunks of R4 *Not split*, are not in it. The page shows it as one line under the split (`costs.dent.incompleteNote`): the parts "are approximate and may hold use that is not theirs". The parts' ranges (R3 step 6) do not cover the folded movement.
- Example (hand-computed, then run through `splitSeat`). The plan is $200 a month, so a point is $0.4615. An earlier chunk set k = 14 ÷ 12 points per $. This chunk is 10 points, R = $4.62, with covey list $2 and outside priced list $1.
  - Complete: U<sub>c</sub> = 10 − 2 − 3.5 = 4.5 points. That is $2.08 unattributed, covey $1.69, outside $0.85.
  - Add **one** unpriced outside call, of any size. Unattributed becomes $0, covey $3.08, outside $1.54.
- A step whose outside calls are *all* unpriced is the contrast. It is not split at all, because "booking R to covey would bill it the outside sessions' work" (`stepReason`, `external.go:2112`).

**Status.**
- *Holds* for the sums. Every identity above adds up, incomplete chunks included. Once #185 is fixed, the list identity gains a *before the reset* part.
- **Breaks the rule for incomplete chunks: [#196](https://github.com/jmarcone/covey/issues/196).** Movement nobody's calls explain is folded into covey's part and the outside part. It is not kept as a part of its own, and it is not inside those parts' ranges.
- Until #196 is fixed, a report must name the number of incomplete chunks next to covey's part. It must not call covey's part a bound while that number is above 0.

### R5 — Currencies and exchange rates are dated

**Rule.**
- Money stays in its currency.
- A conversion to dollars uses a rate a person gave, together with the day that rate is from.
- Amounts in different currencies are never added.
- A monthly limit changed mid-month is priced as Δ(u × L), not Δu × L (H16, see R1).

**How covey computes it.**
- **Usage charges and monthly limits (0151, 0152).** They carry `fx_to_usd` and `fx_date` both or neither (CHECK `credential_usage_charges_fx_chk`, `credential_usage_limits_fx_chk`). A USD row must carry neither, by the same CHECK.
  - A USD amount is its own dollar figure.
  - An amount in another currency has a dollar figure only with both the rate and its date (`limitcharges.go:352-358`, `credcost.go:316-322`). Without them it stays in its currency. The page lists charges per currency and adds them to the seat's real cost only in USD, in the single tile *Real cost, seat + usage*.
- **The bill.** USD only (CHECK `currency = 'USD'` on `billed_cost`).
- **Plans (0145).**
  - A plan in another currency must carry a rate (`Plan.validate`, `internal/runtimes/store.go:532`).
  - The rate is fixed at entry; a new rate is a new plan row from a later date.

**Status.** **Breaks the rule for plans: [#186](https://github.com/jmarcone/covey/issues/186).** `fx_date` is optional on a plan, so a EUR plan with a rate and no date is accepted and prices every point from then on. The page's plan form is USD only, so only the API can enter one today.

### R6 — Data health gates the figures

**Rule.** When a check on the data fails, the affected figure is marked *incomplete*. It does not show a smaller number.

**How covey computes it.** Today only parts of this exist, each in its own place:
- A step not reported by every active machine of a linked source is *not split*. It is never booked to covey (`stepReported`, `external.go:1893`).
- A chunk with unpriced outside calls, or with covey bookings without a line, is *incomplete* (`IncompleteChunks`). It sets no k and has no unattributed part. Its parts are marked only by that count, and they keep a smaller unattributed figure (R4, [#196](https://github.com/jmarcone/covey/issues/196)).
- *Billed* says why instead of $0.00 (no admin key, not mapped, sync failed, not landed). A bill bucket after `data_refreshed_at` is not complete.
- No multiple is shown when points are unpriced, or when there are fewer than 3 points.

**Status.** **Not built: [#178](https://github.com/jmarcone/covey/issues/178).**
- The checks it names (uploader fresh, meter fresh, meter monotonic, no double booking, models priced, cost basis set, bill sync, pre-meter usage) and the trust badge do not exist yet.
- A stopped uploader makes the outside sessions' list look smaller with no mark, because their calls are missing. While its machines count as active (7 days after their last push or call), they hold the meter's readings unsplit. After that, a seat whose only linked source stopped stays unsplit. Where another linked source still reports, though, the readings are split without the stopped source's calls, and the other parts grow.
- Until #178 lands, a report must run those checks by hand. See *Reconciling a figure* below.

## Sources and how they are stored

| Migration | What it holds | Read by |
|---|---|---|
| 0142 `cost_insights` | `model_usage` (one row per result line and model) and `usage_readings` (the meter) | `ModelUsage`, `meterQueryTemplate` |
| 0143 `model_usage_latest` | a view: each session's last line per model (from before the per-line shares of 0144) | no reader in the code today |
| 0144 `cost_deltas` | each line's own share (`basis` = `since_last` or `derived`), the credential and price modifiers per line, `cost_entries.basis` and `booked_usd` | every cost view |
| 0145 `meter_readings` | reading source (`usage_cmd`, `inband`, `manual`), window, reset time; `credential_plans` | `meterSteps`, `CredentialPlans` |
| 0146 `billing` | `billed_cost`, `billed_usage`, `billing_sync`, `credential_billing_keys` | `BilledSeries`, `BilledWindowFor`, `Reconcile` |
| 0147 `cost_gap` | spend of a process that died without a result line, recovered from the next line's running total: `model_usage` rows and cost entries of basis `gap` | every cost view, as part of the lines |
| 0150 `external_usage` | outside calls (`external_usage`), the seat a source shares (`external_usage_meters`), each machine's claim (`external_usage_hosts`) | `externalFor`, `seatSteps`, `hostMarks` |
| 0151 `credential_cost_basis` | `credential_cost_bases`, `credential_usage_charges` | `costBasesFor`, `usageChargesFor` |
| 0152 `credential_usage_limits` | monthly limits; charge kind `export` (shown, never counted) | `meterLimitCharges` |

**Buckets.** All periods are cut in UTC: hours, UTC days, weeks that start Monday UTC, and months.
- 10-minute and 10-hour buckets are cut from the epoch with `date_bin` (`periodSQL`, `internal/observability/observability.go:1127`), so a 10-hour bucket does not start at midnight.
- 10-minute buckets are limited to two days (`maxTenMinuteDays`, `internal/httpapi/handlers.go:1393`).
- The meter and the bill exist per hour at the finest, so at 10 minutes they are off and say why (`bucket_below_source`).

**Sessions outside covey are included by default.** Every tile, chart and table counts them, and each figure names its two parts. `?only=covey` (the *covey only* chip) leaves them out of the tiles, chart and tables. The headline keeps the whole dent.

### `POST /api/v1/cost/external-usage`

Who may call it: `POST` and `DELETE` need an `org_admin` or security seat (browser session or API key); `GET` is open to any role (`internal/httpapi/server.go:754-757`).
- **Limits.** At most 2 MiB and 5,000 rows per request. Unknown fields are refused, so no transcript text can ride along.
- **Body.**
  - `source`: 1 to 64 characters.
  - `meter_credential`:
    - absent: the stored link is unchanged;
    - `null`: the link is removed;
    - `{"secret_key", "secret_slot"}`: the link is set.
  - `rows[]`: each row has `host`, `session_id`, `message_id`, `ts`, `model`, the five token counts, an optional `origin` (`main`, `subagent` or `workflow`) and an optional `reported_usd`, a non-negative amount below 10<sup>7</sup>.
  - `reported_through`: an object from host to time.
- **Row rules.**
  - Every count is required and must be between 0 and 10<sup>9</sup>.
  - `ts` may be at most 5 minutes ahead of the server's clock.
  - Text fields are at most 256 bytes and contain no NUL.
  - A `message_id` may appear once per request.
  - Rows are upserted on (organisation, source, message_id), so sending them again changes nothing.
- **`reported_through` (the watermark).** It is the host's claim that every call up to that time has been sent.
  - A time after the push is clamped to the push's arrival.
  - The stored value is never moved back (`GREATEST`).
  - `first_reported_at` is set once.
  - A machine counts as *active* for 7 days after its last push or call.
  - A step is split only when every active machine of every linked source has a watermark at or after the reading. An active machine without a watermark therefore holds the reading.
  - A machine that never sent a watermark, and has neither pushed nor called since its source began sending them, is a name from an older uploader and is left out.
  - A source may have at most 100 active hosts.
- **Response (`ExternalUploadResult`).** `source`, `received`, `inserted`, `updated`, `unchanged` (the last three add up to `received`), `meter_credential`, and `hosts[]` with each machine's `host`, `reported_through`, `first_reported_at` and `pushed_at`, as stored after the upload.
- **Other methods.**
  - `DELETE ?source=` (`org_admin` or security) removes a source's rows, its meter link and its hosts' watermarks.
  - `GET ?days=&bucket=&source=` (any role) returns the calls per bucket, source, origin and model (`ExternalUsageByModel`).

How to run the uploader is in [`docs/en/operations/usage-push.md`](../docs/en/operations/usage-push.md).

### `GET /api/v1/cost/cells`: the `external` field

- **`sources[]`** (per source):
  - `meter_credential`: the link is not dated and applies to every call of the source;
  - `calls`;
  - `list_usd`, `reported_usd`, `lead_usd`: each `null` when there is no figure, never 0;
  - `unpriced_*` and `lead_unpriced_*`;
  - `first_at`, `last_at`;
  - `hosts[]`.
- **`cells[]`** (per bucket × source × origin): calls, list by cost type (`measured`), reported and lead figures, tokens per type.
- **`meter`** (`ExternalMeter`):
  - `steps[]`, one per reading of a seat. Fields:
    - `from`, `at`, `points`, `reset`;
    - `real_usd`: `null` when unpriced or under the usage basis;
    - the two lists and their lead twins;
    - `reported`, `split`, `reason`: one of `not_reported`, `moved_without_reported_call`, `outside_unpriced`, `unpriced`, `idle`, `usage`;
    - the parts `external_real_usd`, `covey_real_usd`, `unattributed_real_usd`, `unsplit_real_usd`;
    - `source_parts`.
  - `hours[]`: the steps summed per UTC hour.
  - `credentials[]`: one per seat (`ExternalMeterTotal`). Fields:
    - `meter_source`: the reading source with the most readings; on a tie, `usage_cmd` before `manual` before `inband`;
    - `sources`;
    - `points_per_list_usd` (k at the latest plan price) and `plan_rates[]` (k per plan price);
    - `agents[]` (covey's part per agent);
    - all `ExternalMeterSums` fields.
  - The whole seat's `ExternalMeterSums`, the same fields again for all seats together:
    - `real_usd` and its four parts;
    - `points`, `split_points`;
    - the slack fields;
    - `floor_list_usd` / `floor_lead_usd`;
    - the `unmetered_*` lists (before / after the readings);
    - `unpriced_*`, `usage_*`;
    - `incomplete_chunks`;
    - `split_runs` (the number of chunks).
  - A sum over seats is a single figure only while `point_rate_mixed` is false.

### Cost basis per credential (0151)

| Basis | The money is | Effect on the meter |
|---|---|---|
| `seat` (default; a credential without a row) | the plan price | points priced as in R1 |
| `usage` | the usage charges | points read under it are `usage_points`: out of the real cost, the split and every chunk; not unpriced |
| `seat_and_usage` | both | the seat from the meter; the charges beside it, added only in USD |

- A basis is dated and keyed on the secret value. A change adds a row, and no row is overwritten.
- Charges count only for the time a usage basis held, pro rata at the edges (`chargeInWindow`, `credcost.go:265`).
- Over spans the monthly-limit meter priced, the meter is the money: a hand-entered charge over those spans is shown as superseded and is not counted again (`usageChargesFor`, `credcost.go:139`).
- A charge of kind `export` is a provider export for checking. It is shown and never counted.

### `GET /api/v1/cost/billed`: `hourly_from` and `whole_days`

At `hour` and `10hour`, `hourly_from` is where the hourly rows of the bill begin. The hourly sync reaches back 48 hours, and before that the bill is known per day only.
- Days read from daily rows go to `whole_days`, one point at UTC midnight, never spread into hours.
- A 10-hour bucket that starts before `hourly_from`, or overlaps a whole day of its key, is not complete.
- `points` and `whole_days` together are the rows the tile and `/cost/reconcile` read.
- At `10min` the series is empty with `reason: bucket_below_source`.

## Reconciling a figure: SQL, API, page

Before a figure goes into a report, it is reproduced three ways: by a query over the raw rows, from the API's JSON, and from what the page shows. The three must agree, or the difference is explained.

**Meter points of one seat** over [`:since`, `:until`), per week. This is `meterQueryTemplate` (`stacked.go:643`) narrowed to one seat, one source and the `seven_day` window, with the same rules as R1: the 8-day look-back, the drop of a late reading, the three ways a new week starts and the running maximum. A reading more than 8 days before `:since` is not read, so when the seat has no reading in those 8 days the first one in the range is the baseline and counts 0, as on the page. Multiply by the price per point of R1. The range is ±1 point per week.

```sql
WITH r0 AS (
  SELECT id, fetched_at, week_percent AS pct, resets_at,
         MAX(resets_at) OVER (ORDER BY fetched_at, id
           ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS seen_resets
  FROM usage_readings
  WHERE org_id = :org AND secret_key = :key AND secret_slot = :slot
    AND source = :source AND window_kind = 'seven_day'
    AND supported AND NOT stale AND week_percent IS NOT NULL
    AND fetched_at >= :since::timestamptz - interval '8 days'
), r AS (
  SELECT * FROM r0
  WHERE resets_at IS NULL OR seen_resets IS NULL OR resets_at >= seen_resets - interval '1 hour'
), l AS (
  SELECT r.*, LAG(pct) OVER w AS prev_pct, LAG(resets_at) OVER w AS prev_resets
  FROM r WINDOW w AS (ORDER BY fetched_at, id)
), f AS (
  SELECT l.*, CASE
    WHEN prev_pct IS NULL THEN 0
    WHEN resets_at IS NOT NULL AND prev_resets IS NOT NULL
      THEN (resets_at > prev_resets + interval '1 hour')::int
    WHEN prev_resets IS NOT NULL THEN (prev_resets <= fetched_at)::int
    WHEN pct < prev_pct - 1 THEN 1
    ELSE 0 END AS reset
  FROM l
), m AS (
  SELECT f.*, MAX(pct) OVER (PARTITION BY week ORDER BY fetched_at, id) AS top
  FROM (SELECT f.*, SUM(reset) OVER (ORDER BY fetched_at, id) AS week FROM f) f
), s AS (
  SELECT fetched_at, week, CASE
    WHEN prev_pct IS NULL THEN 0
    WHEN reset = 1 THEN top
    ELSE top - LAG(top) OVER (ORDER BY fetched_at, id) END AS points
  FROM m
)
SELECT week, MIN(fetched_at) AS first_at, SUM(points) AS points,
       GREATEST(SUM(points) - 1, 0) AS low, SUM(points) + 1 AS high
FROM s WHERE fetched_at >= :since AND fetched_at < :until
GROUP BY week ORDER BY week;
```

Checked on a scratch table against the code's query and against hand-computed values, seven seats, all three equal:

| Seat | Readings | Points |
|---|---|---|
| A | manual, 20 % (reset 10:10), 25 % (10:10), 30 % (10:40), range from the second | 10 (a cut by the rounded reset hour would give 5 + 30 = 35) |
| B | 40 %, a new week at 3 %, a late reading 45 % of the old week, 6 % | 6 (the late reading dropped) |
| C | one reading 9 days before the range, then 10 %, 14 % | 4 (10 % is the baseline) |
| D | 30 %, 32 %, a stale 90 %, 31 %, 33 %, 40 % after `:until` | 3 |
| E | no reset times: 6 %, 9 %, 8 %, 2 %, 4 % | 3 + 4 = 7 (a drop of 1 is lag, a drop of 6 a new week) |
| F | 50 % with a reset that has passed, then 3 %, 5 % without | 5 |
| G | 50 % with a reset still ahead, then 49 %, 2 % without | 0 + 2 = 2 |

**List and reported price of covey's lines** on one credential in a span (a step's L<sub>cov</sub>). A model the table does not know comes out with an empty list and its reported figure. The prices are those of `listprice.go`.

```sql
WITH prices (id, input, output, cache_read, w5m, w1h) AS (VALUES
  ('claude-opus-5-5', 4, 20, 0.20, 5, 8), ('claude-sonnet-5-5', 2, 10, 0.10, 2.5, 4),
  ('claude-haiku-5-5', 0.10, 0.50, 0.01, 0.125, 0.20)),
m AS (
  SELECT u.*, (SELECT p.id FROM prices p
               WHERE lower(regexp_replace(u.model, '\[.*$', '')) = p.id
                  OR lower(regexp_replace(u.model, '\[.*$', '')) LIKE p.id || '-%'
               ORDER BY length(p.id) DESC LIMIT 1) AS priced_as
  FROM model_usage u
  WHERE u.org_id = :org AND u.secret_key = :key AND u.secret_slot = :slot
    AND u.created_at > :from AND u.created_at <= :to)
SELECT COALESCE(m.priced_as, m.model) AS model,
       ROUND(SUM((m.input_tokens * p.input + m.output_tokens * p.output + m.cache_read_tokens * p.cache_read
            + m.cache_write_5m_tokens * p.w5m + m.cache_write_1h_tokens * p.w1h) / 1e6
            + m.web_search_requests * 0.01), 6) AS list_usd,
       SUM(m.engine_usd) AS reported_usd
FROM m LEFT JOIN prices p ON p.id = m.priced_as
GROUP BY 1 ORDER BY 1;
```

**The bill of one API key per UTC day.** This reads daily rows only, so it is complete for days the daily re-read has covered. The page reads, per UTC day, the width fetched last (hourly on a tie; `chosenRows`, `billing.go:381`); the hourly sync reaches back 48 hours to a UTC midnight (`billedHourlyReach`, `billing.go:852`), so for the last two or three days compare against the hourly rows summed per UTC day. Do the same for a day on which one API key maps to several credentials (`chosenRows`, `billing.go:405`): once that day's hours are complete the page reads its hourly rows.

```sql
SELECT (bucket_start AT TIME ZONE 'UTC')::date AS day, SUM(amount_cents) / 100 AS billed_usd,
       SUM(list_amount_cents) / 100 AS billed_list_usd, MAX(data_refreshed_at) AS complete_to
FROM billed_cost
WHERE org_id = :org AND bucket_width = '1d' AND api_key_id = :api_key_id
  AND bucket_start >= :since
GROUP BY 1 ORDER BY 1;
```

**From the API.** These two must agree:
- `jq '.external.meter | .real_usd - (.covey_real_usd + .external_real_usd + .unattributed_real_usd + .unsplit_real_usd)'` on `/api/v1/cost/cells?days=7&bucket=hour` must be 0 to the cent (R4).
- `.external.meter.credentials[] | {points, real_usd}` must match the meter query times the price per point.

**Until #178 lands, check by hand before a report:**
- every host that pushed in the last 7 days has pushed in the last 75 minutes (`external_usage_hosts.pushed_at`);
- the meter has a reading in the last hour of the period;
- every model in `model_usage` and `external_usage` has a list price;
- no reset falls inside a step that the report's multiple divides (#185);
- `.external.meter.credentials[].incomplete_chunks` is 0 for every seat whose split the report quotes. Where it is not, name the count next to covey's part (#196).

## Open points

| Issue | Rule | What is missing |
|---|---|---|
| [#185](https://github.com/jmarcone/covey/issues/185) | R2, R3 | a reset step's old-week list is in the multiple and the split |
| [#196](https://github.com/jmarcone/covey/issues/196) | R4, R3 | an incomplete chunk's unexplained movement is folded into covey's and the outside part |
| [#186](https://github.com/jmarcone/covey/issues/186) | R5 | a plan's exchange rate needs no date |
| [#178](https://github.com/jmarcone/covey/issues/178) | R6 | data checks and the *incomplete* mark |
| [#167](https://github.com/jmarcone/covey/issues/167) | R1 | headline and multiple for pay-per-token credentials; real cost from the monthly meter until the bill lands |
| [#153](https://github.com/jmarcone/covey/issues/153) | R1, R2 | readings only while covey runs |
| [#177](https://github.com/jmarcone/covey/issues/177) | R2 | list per point varies about 10× with the workload mix (unexplained) |
| [#154](https://github.com/jmarcone/covey/issues/154) | R1 | models without a list price |
| [#158](https://github.com/jmarcone/covey/issues/158) | R3 | F3c edge cases |
