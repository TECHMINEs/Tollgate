package tariff

import (
	"errors"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func at(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

func rule() DetentionRule {
	return DetentionRule{FreeMinutes: 120, RatePerHourCents: 7_500, IncrementMinutes: 15, CapPerStopCents: 60_000, LateGraceMinutes: 30}
}

func TestLinehaulUsesMinimum(t *testing.T) {
	l := Lane{ID: "L1", RatePerMileCents: 250, MinimumCents: 45_000}
	if got := LinehaulCents(l, 100); got != 45_000 {
		t.Fatalf("short haul: got %v, want the $450 minimum", got)
	}
	if got := LinehaulCents(l, 300); got != 75_000 {
		t.Fatalf("300 mi x $2.50: got %v", got)
	}
	if got := LinehaulCents(Lane{FlatCents: 90_000}, 999); got != 90_000 {
		t.Fatalf("flat lane: got %v", got)
	}
}

func TestFuelBands(t *testing.T) {
	s := FuelSchedule{Bands: []FuelBand{{300, 325, 1600}, {325, 350, 1700}}}
	cases := []struct {
		price int64
		want  Cents
	}{{300, 16_000}, {324, 16_000}, {325, 17_000}, {349, 17_000}}
	for _, c := range cases {
		got, _, err := FuelCents(s, 100_000, c.price)
		if err != nil || got != c.want {
			t.Errorf("price %d: got %v, %v; want %v", c.price, got, err, c.want)
		}
	}
	if _, _, err := FuelCents(s, 100_000, 350); !errors.Is(err, ErrFuelPriceOutOfTable) {
		t.Fatalf("price at upper bound should be out of table, got %v", err)
	}
}

func TestDetention(t *testing.T) {
	cases := []struct {
		name              string
		r                 DetentionRule
		appt, arr, dep    time.Time
		want              Cents
		increments        int
		forfeited, capped bool
	}{
		{"inside free time", rule(), at(0), at(0), at(120), 0, 0, false, false},
		{"partial increment rounds down", rule(), at(0), at(0), at(120 + 29), 1_875, 1, false, false},
		{"two increments", rule(), at(0), at(0), at(150), 3_750, 2, false, false},
		{"round up", func() DetentionRule { r := rule(); r.RoundUp = true; return r }(), at(0), at(0), at(121), 1_875, 1, false, false},
		{"early arrival waits for the appointment", rule(), at(0), at(-60), at(150), 3_750, 2, false, false},
		{"late within grace still earns", rule(), at(0), at(30), at(180), 3_750, 2, false, false},
		{"late past grace forfeits", rule(), at(0), at(31), at(400), 0, 0, true, false},
		{"cap", rule(), at(0), at(0), at(120 + 60*12), 60_000, 48, false, true},
		{"no appointment", rule(), time.Time{}, at(0), at(135), 1_875, 1, false, false},
		{"departure before arrival", rule(), at(0), at(10), at(5), 0, 0, false, false},
	}
	for _, c := range cases {
		got := DetentionCents(c.r, c.appt, c.arr, c.dep)
		if got.AmountCents != c.want || got.Increments != c.increments || got.Forfeited != c.forfeited || got.Capped != c.capped {
			t.Errorf("%s: got %+v; want %v, %d increments, forfeited=%v capped=%v", c.name, got, c.want, c.increments, c.forfeited, c.capped)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Tariff{ID: "t", Version: 1, CarrierID: "c",
		Lanes:     []Lane{{ID: "L1", RatePerMileCents: 250}},
		Fuel:      FuelSchedule{Bands: []FuelBand{{300, 325, 1600}, {325, 350, 1700}}},
		Detention: rule()}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid tariff rejected: %v", err)
	}
	bad := ok
	bad.Lanes = []Lane{{ID: "L1", RatePerMileCents: 250, FlatCents: 100}, {ID: "L1", FlatCents: 1}}
	bad.Fuel.Bands = []FuelBand{{300, 325, 1600}, {330, 350, 1700}}
	if err := bad.Validate(); err == nil {
		t.Fatal("ambiguous lanes and a gap in the fuel table should be rejected")
	}
}

func TestCentsString(t *testing.T) {
	if s := Cents(-123_45).String(); s != "-$123.45" {
		t.Fatalf("got %s", s)
	}
}
