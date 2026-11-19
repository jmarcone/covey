package orchestrator

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"covey/internal/billing"
	"covey/internal/observability"
	"covey/internal/secrets"
)

// The bill, read from Anthropic (spec/06, migration 0146).
//
// For every organisation that stored an anthropic_admin_key, the Usage and
// Cost Analytics API is read once an hour: the last two days in hourly
// buckets — from the UTC midnight before, so that a day's hourly rows always
// come from one read of the whole day — and once a day 35 days in daily
// buckets, which picks up what Anthropic revised since. Each window replaces
// its rows in one transaction, and only once every page of both endpoints has
// arrived; a read that fails keeps the rows of the last one that did not, and
// says why in billing_sync.
//
// The key is read here, handed to the client, and goes nowhere else: not into
// a log line, not into last_error, never towards a sandbox (it is a reserved
// key, internal/secrets).

const (
	// billingEvery: the bill lands 4-24 h late; an hour is plenty.
	billingEvery = time.Hour
	// billingDelay: the first read after a start waits, like the credential
	// probe, so that a restart does not open with a burst of requests.
	billingDelay = 3 * time.Minute
	// billingHourlyBack is the hourly read's reach (from the midnight before).
	billingHourlyBack = 48 * time.Hour
	// billingDailyBack / billingDailyEvery: the re-read of revisions.
	billingDailyBack  = 35 * 24 * time.Hour
	billingDailyEvery = 24 * time.Hour
	// billingTimeout bounds one organisation's sync.
	billingTimeout = 10 * time.Minute
)

// ErrNoAdminKey: the organisation stored no anthropic_admin_key.
var ErrNoAdminKey = errors.New("no anthropic_admin_key is stored for this organisation")

// costGroups / usageGroups: what the bill is split by. There is no filter on
// api_key_ids: keys covey does not know show up as other use of the account.
var (
	usageGroups = []string{"model", "api_key_id", "workspace_id", "service_tier"}
	costGroups  = append(append([]string{}, usageGroups...), "cost_type", "token_type")
)

// billingLoop reads the bill once an hour.
func (o *Orchestrator) billingLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(billingDelay):
	}
	t := time.NewTicker(billingEvery)
	defer t.Stop()
	o.SyncBilling(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.SyncBilling(ctx)
		}
	}
}

// SyncBilling reads the bill of every organisation with an admin key.
// Exported so that a test or a command can run it without waiting an hour.
func (o *Orchestrator) SyncBilling(ctx context.Context) {
	if o.Secrets == nil || o.Obs == nil {
		return
	}
	stored, err := o.Secrets.List(ctx, uuid.Nil, secrets.AdminKey)
	if err != nil {
		o.Log.Warn("billing sync: admin keys not listed", "err", err)
		return
	}
	seen := map[uuid.UUID]bool{}
	for _, st := range stored {
		if ctx.Err() != nil {
			return
		}
		// Org-wide only, and the exact key (List matches a suffix).
		if st.AgentID != nil || st.Key != secrets.AdminKey || seen[st.OrgID] {
			continue
		}
		seen[st.OrgID] = true
		_ = o.SyncBillingOrg(ctx, st.OrgID, false)
	}
}

// SyncBillingOrg reads one organisation's bill: the hourly window always, the
// 35-day re-read when the last one is a day old (or forceDaily). The outcome
// is recorded in billing_sync either way; the error is returned for the admin
// button.
func (o *Orchestrator) SyncBillingOrg(ctx context.Context, orgID uuid.UUID, forceDaily bool) error {
	if o.Secrets == nil || o.Obs == nil {
		return ErrNoAdminKey
	}
	key, err := o.Secrets.Get(ctx, orgID, secrets.AdminKey)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && strings.TrimSpace(key) == "") {
		return ErrNoAdminKey
	}
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	ctx, cancel := context.WithTimeout(ctx, billingTimeout)
	defer cancel()

	client := o.billingClient(key)
	now := time.Now().UTC()
	if client.Now != nil {
		now = client.Now().UTC()
	}
	st, err := o.Obs.BillingSync(ctx, orgID)
	if err != nil {
		return err
	}
	daily := forceDaily || st.LastDailyAt == nil || now.Sub(*st.LastDailyAt) >= billingDailyEvery

	// Both reads are stamped with the sync's one instant, not with the
	// moment each was written. The daily re-read is written seconds after
	// the hourly one; stamped with its own time it was the freshest width
	// for every day of the hourly reach until the next hourly sync, and the
	// hourly views fell back to one point per day (chosenRows takes the
	// freshest width, the hours on a tie).
	attempt := observability.BillingSyncAttempt{At: now, Status: "ok"}
	refreshed, landed, err := o.syncBillingWindow(ctx, client, orgID, billing.Hour,
		now.Add(-billingHourlyBack).Truncate(24*time.Hour), now, now)
	if err == nil && daily {
		var dailyLanded bool
		_, dailyLanded, err = o.syncBillingWindow(ctx, client, orgID, billing.Day, now.Add(-billingDailyBack), now, now)
		landed = landed || dailyLanded
		attempt.Daily = err == nil
	}
	attempt.DataRefreshedAt = refreshed
	switch {
	case err != nil:
		// The client's errors are scrubbed already; this is the second net.
		attempt.Status, attempt.Error = "failed", strings.ReplaceAll(err.Error(), key, "[redacted]")
		o.Log.Warn("billing sync failed", "org", orgID, "err", attempt.Error)
	case !landed:
		attempt.Status = "not_landed"
	}
	if nerr := o.Obs.NoteBillingSync(context.WithoutCancel(ctx), orgID, attempt); nerr != nil {
		o.Log.Warn("billing sync: state not recorded", "org", orgID, "err", nerr)
	}
	if err != nil {
		return errors.New(attempt.Error)
	}
	return nil
}

// syncBillingWindow reads both endpoints over one window and replaces the
// stored rows with them — only after both arrived complete — stamped
// fetchedAt, the sync's instant.
func (o *Orchestrator) syncBillingWindow(ctx context.Context, c *billing.Client, orgID uuid.UUID, width string,
	from, to, fetchedAt time.Time) (*time.Time, bool, error) {
	cost, err := c.Cost(ctx, billing.Query{Start: from, End: to, Bucket: width, GroupBy: costGroups})
	if err != nil {
		return nil, false, err
	}
	usage, err := c.Usage(ctx, billing.Query{Start: from, End: to, Bucket: width, GroupBy: usageGroups})
	if err != nil {
		return nil, false, err
	}
	if err := o.Obs.ReplaceBilled(ctx, orgID, width, &cost, &usage, fetchedAt); err != nil {
		return nil, false, err
	}
	refreshed := cost.DataRefreshedAt
	if u := usage.DataRefreshedAt; u != nil && (refreshed == nil || u.Before(*refreshed)) {
		refreshed = u
	}
	return refreshed, cost.Landed || usage.Landed, nil
}

// billingClient builds the client for one key; tests point it elsewhere.
func (o *Orchestrator) billingClient(key string) *billing.Client {
	if o.BillingClient != nil {
		return o.BillingClient(key)
	}
	return &billing.Client{Key: key}
}
