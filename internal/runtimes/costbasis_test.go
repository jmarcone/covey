package runtimes

import (
	"math"
	"strings"
	"testing"
	"time"
)

// A usage charge is validated like a plan: a three-letter currency, a
// non-negative finite amount, a span that ends after it begins — and a rate
// only with its date.
func TestUsageChargeValidate(t *testing.T) {
	from := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	ok := UsageCharge{Amount: 180, Currency: " eur ", From: from, To: to, Note: "  measured  "}
	if err := ok.Validate(); err != nil || ok.Currency != "EUR" || ok.Note != "measured" || ok.FXToUSD != nil {
		t.Fatalf("a EUR charge without a rate: %v %+v", err, ok)
	}
	rated := UsageCharge{Amount: 180, Currency: "EUR", From: from, To: to, FXToUSD: f(1.08), FXDate: s("2026-11-18")}
	if err := rated.Validate(); err != nil {
		t.Fatalf("a EUR charge with a rate and its date: %v", err)
	}
	usd := UsageCharge{Amount: 5, Currency: "USD", From: from, To: to, FXToUSD: f(1), FXDate: s("2026-11-18")}
	if err := usd.Validate(); err != nil || usd.FXToUSD != nil || usd.FXDate != nil {
		t.Fatalf("a USD charge keeps no rate: %v %+v", err, usd)
	}
	for name, c := range map[string]UsageCharge{
		"currency":        {Amount: 1, Currency: "EURO", From: from, To: to},
		"currency digits": {Amount: 1, Currency: "E1R", From: from, To: to},
		"empty currency":  {Amount: 1, Currency: "", From: from, To: to},
		"negative":        {Amount: -1, Currency: "EUR", From: from, To: to},
		"nan":             {Amount: math.NaN(), Currency: "EUR", From: from, To: to},
		"inf":             {Amount: math.Inf(1), Currency: "EUR", From: from, To: to},
		"to before from":  {Amount: 1, Currency: "EUR", From: to, To: from},
		"empty span":      {Amount: 1, Currency: "EUR", From: from, To: from},
		"no to":           {Amount: 1, Currency: "EUR", From: from},
		"rate no date":    {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1)},
		"date no rate":    {Amount: 1, Currency: "EUR", From: from, To: to, FXDate: s("2026-11-18")},
		"bad date":        {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1), FXDate: s("18.11.2026")},
		"zero rate":       {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(0), FXDate: s("2026-11-18")},
		"usd not at 1":    {Amount: 1, Currency: "USD", From: from, To: to, FXToUSD: f(0.9), FXDate: s("2026-11-18")},
		"long note":       {Amount: 1, Currency: "EUR", From: from, To: to, Note: strings.Repeat("x", 2001)},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, c)
		} else if _, ok := err.(InvalidCost); !ok {
			t.Errorf("%s: %T, want InvalidCost", name, err)
		}
	}
}

func TestValidBasis(t *testing.T) {
	for _, b := range []string{"seat", "usage", "seat_and_usage"} {
		if !ValidBasis(b) {
			t.Errorf("%s refused", b)
		}
	}
	for _, b := range []string{"", "Seat", "both", "seat+usage"} {
		if ValidBasis(b) {
			t.Errorf("%q accepted", b)
		}
	}
}

// What the columns cannot hold is refused before it reaches them, as a
// sentence (400), not as the database's error (500): NUMERIC(14,4) for the
// amount, NUMERIC(12,6) > 0 for the rate, and a rate date of a day that was.
func TestUsageChargeValidateColumnBounds(t *testing.T) {
	from := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02")
	for name, c := range map[string]UsageCharge{
		"largest amount": {Amount: MaxChargeAmount, Currency: "EUR", From: from, To: to},
		"smallest rate":  {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(MinFXToUSD), FXDate: s("2026-11-18")},
		"largest rate":   {Amount: 1, Currency: "JPY", From: from, To: to, FXToUSD: f(MaxFXToUSD), FXDate: s("2026-11-18")},
		"first day":      {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1), FXDate: s("2000-01-01")},
		"tomorrow":       {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1), FXDate: s(tomorrow)},
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, c := range map[string]UsageCharge{
		"amount too large": {Amount: 1e10, Currency: "EUR", From: from, To: to},
		"rate rounds to 0": {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(4e-7), FXDate: s("2026-11-18")},
		"rate too large":   {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1e6), FXDate: s("2026-11-18")},
		"rate nan":         {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(math.NaN()), FXDate: s("2026-11-18")},
		"rate inf":         {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(math.Inf(1)), FXDate: s("2026-11-18")},
		"date before 2000": {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1), FXDate: s("1999-12-31")},
		"date in a year":   {Amount: 1, Currency: "EUR", From: from, To: to, FXToUSD: f(1.1), FXDate: s(time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02"))},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		} else if _, ok := err.(InvalidCost); !ok {
			t.Errorf("%s: %T, want InvalidCost", name, err)
		}
	}
}
