// Package ledger turns audit outcomes into double-entry journal postings.
//
// The ledger is append-only. When a verdict or accrual changes, the previous version is
// reversed and the new one booked in the same journal version, the way an accountant
// corrects an entry. Replaying the pipeline therefore never double-books.
package ledger

import (
	"fmt"
	"slices"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
)

type Cents = tariff.Cents

// Account is a general-ledger account in the tenant's freight sub-ledger.
type Account string

const (
	FreightExpense  Account = "6100_FREIGHT_EXPENSE"
	DisputeSuspense Account = "1450_FREIGHT_DISPUTES" // asset: amounts billed that we claim back
	AccruedFreight  Account = "2150_ACCRUED_FREIGHT"  // liability: delivered, not yet invoiced
	APCarriers      Account = "2010_AP_CARRIERS"      // liability: approved to pay
	APDisputed      Account = "2015_AP_DISPUTED"      // liability: billed but withheld
)

// Line is one side of a journal entry. Exactly one of Debit or Credit is non-zero.
type Line struct {
	Account Account `json:"account"`
	Debit   Cents   `json:"debit_cents"`
	Credit  Cents   `json:"credit_cents"`
	Memo    string  `json:"memo"`
}

// Posting is a line booked to a journal version.
type Posting struct {
	JournalKey string `json:"journal_key"`
	Version    int    `json:"version"`
	LineNo     int    `json:"line_no"`
	Line
}

// Balanced reports whether debits equal credits.
func Balanced(lines []Line) bool {
	var d, c Cents
	for _, l := range lines {
		d += l.Debit
		c += l.Credit
	}
	return d == c
}

func dr(a Account, amt Cents, memo string) Line { return Line{Account: a, Debit: amt, Memo: memo} }
func cr(a Account, amt Cents, memo string) Line { return Line{Account: a, Credit: amt, Memo: memo} }

// AccrualKey is the journal for a shipment's delivered-not-invoiced accrual.
func AccrualKey(shipmentID string) string { return "accrual:" + shipmentID }

// InvoiceKey is the journal for an invoice's booked outcome.
func InvoiceKey(invoiceID string) string { return "invoice:" + invoiceID }

// AccrualLines accrues the expected cost of a delivered shipment.
func AccrualLines(shipmentID string, amount Cents) []Line {
	if amount <= 0 {
		return nil
	}
	memo := "accrue delivered freight " + shipmentID
	return []Line{dr(FreightExpense, amount, memo), cr(AccruedFreight, amount, memo)}
}

// VerdictLines books an invoice verdict. A held invoice books nothing.
func VerdictLines(v audit.Verdict) []Line {
	memo := fmt.Sprintf("%s %s %s", v.Decision, v.CarrierID, v.InvoiceNumber)
	var out []Line
	add := func(l Line) {
		if l.Debit > 0 || l.Credit > 0 {
			out = append(out, l)
		}
	}
	switch v.Decision {
	case audit.Approve, audit.ApproveVariance, audit.ShortPay:
		add(dr(FreightExpense, v.ApprovedCents, memo))
		add(dr(DisputeSuspense, v.DisputedCents, memo))
		add(cr(APCarriers, v.ApprovedCents, memo))
		add(cr(APDisputed, v.DisputedCents, memo))
	case audit.Reject:
		add(dr(DisputeSuspense, v.DisputedCents, memo))
		add(cr(APDisputed, v.DisputedCents, memo))
	}
	return out
}

// Rebook moves a journal from its current lines to next. It returns nil when nothing
// changed, so replays are no-ops. Otherwise it returns one balanced journal version
// that reverses every current line and books every next line.
func Rebook(key string, curVersion int, cur, next []Line) []Posting {
	if slices.Equal(cur, next) {
		return nil
	}
	version := curVersion + 1
	var out []Posting
	for _, l := range cur {
		out = append(out, Posting{JournalKey: key, Version: version, LineNo: len(out) + 1, Line: Line{
			Account: l.Account, Debit: l.Credit, Credit: l.Debit, Memo: fmt.Sprintf("reverse v%d: %s", curVersion, l.Memo),
		}})
	}
	for _, l := range next {
		out = append(out, Posting{JournalKey: key, Version: version, LineNo: len(out) + 1, Line: l})
	}
	return out
}

// Balances sums postings per account as debit minus credit.
func Balances(ps []Posting) map[Account]Cents {
	out := map[Account]Cents{}
	for _, p := range ps {
		out[p.Account] += p.Debit - p.Credit
	}
	return out
}
