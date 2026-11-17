package ledger

import (
	"testing"

	"github.com/TECHMINEs/Tollgate/internal/audit"
)

func TestVerdictLinesBalance(t *testing.T) {
	for _, v := range []audit.Verdict{
		{Decision: audit.Approve, ApprovedCents: 60_000},
		{Decision: audit.ShortPay, ApprovedCents: 60_000, DisputedCents: 11_250},
		{Decision: audit.Reject, DisputedCents: 60_000},
		{Decision: audit.Hold},
	} {
		if ls := VerdictLines(v); !Balanced(ls) {
			t.Errorf("%s does not balance: %+v", v.Decision, ls)
		}
	}
	if ls := VerdictLines(audit.Verdict{Decision: audit.Hold}); len(ls) != 0 {
		t.Error("a held invoice books nothing")
	}
}

func TestRebookReversesAndRebooks(t *testing.T) {
	v1 := VerdictLines(audit.Verdict{Decision: audit.ShortPay, ApprovedCents: 60_000, DisputedCents: 11_250})
	v2 := VerdictLines(audit.Verdict{Decision: audit.Approve, ApprovedCents: 71_250})

	if ps := Rebook("invoice:i1", 1, v1, v1); ps != nil {
		t.Fatal("unchanged lines must not post anything")
	}
	first := Rebook("invoice:i1", 0, nil, v1)
	second := Rebook("invoice:i1", 1, v1, v2)
	if len(second) != len(v1)+len(v2) || second[0].Version != 2 {
		t.Fatalf("rebook should reverse %d lines and book %d at v2: %+v", len(v1), len(v2), second)
	}
	var lines []Line
	for _, p := range second {
		lines = append(lines, p.Line)
	}
	if !Balanced(lines) {
		t.Fatal("rebook journal must balance")
	}
	bal := Balances(append(first, second...))
	if bal[FreightExpense] != 71_250 || bal[APCarriers] != -71_250 || bal[DisputeSuspense] != 0 || bal[APDisputed] != 0 {
		t.Fatalf("net balances should equal v2 alone: %v", bal)
	}
}

func TestAccrual(t *testing.T) {
	if AccrualLines("s1", 0) != nil {
		t.Fatal("zero accrual books nothing")
	}
	if !Balanced(AccrualLines("s1", 5)) {
		t.Fatal("accrual must balance")
	}
}
