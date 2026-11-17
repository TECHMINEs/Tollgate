// Package tariff models a carrier's contracted rates and rates individual charges.
//
// Every function here is pure: the same tariff and inputs always give the same amount.
// Money is integer cents throughout; nothing in Tollgate uses floating point for money.
package tariff

import (
	"errors"
	"fmt"
	"time"
)

// Cents is an amount of money in the tariff's currency, in minor units.
type Cents int64

func (c Cents) String() string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// ChargeCode identifies a line on a freight invoice.
type ChargeCode string

const (
	Linehaul  ChargeCode = "LINEHAUL"
	Fuel      ChargeCode = "FUEL"
	Detention ChargeCode = "DETENTION"
	Liftgate  ChargeCode = "LIFTGATE"
	StopOff   ChargeCode = "STOP_OFF"
	Lumper    ChargeCode = "LUMPER"
)

// Contracted reports whether a charge code can appear on a Tollgate tariff at all.
func Contracted(c ChargeCode) bool {
	switch c {
	case Linehaul, Fuel, Detention, Liftgate, StopOff, Lumper:
		return true
	}
	return false
}

// Tariff is one immutable version of a shipper–carrier rate agreement.
// A shipment pins the tariff version that was in force when it was tendered.
type Tariff struct {
	ID        string `json:"id"`
	Version   int    `json:"version"`
	CarrierID string `json:"carrier_id"`
	Currency  string `json:"currency"`

	Lanes     []Lane        `json:"lanes"`
	Fuel      FuelSchedule  `json:"fuel"`
	Detention DetentionRule `json:"detention"`

	LiftgateCents  Cents `json:"liftgate_cents"`
	StopOffCents   Cents `json:"stop_off_cents"`   // per stop between first pickup and final delivery
	LumperCapCents Cents `json:"lumper_cap_cents"` // pass-through, receipt required
}

// Lane is a contracted origin–destination rate.
type Lane struct {
	ID               string `json:"id"`
	Origin           string `json:"origin"`
	Destination      string `json:"destination"`
	RatePerMileCents Cents  `json:"rate_per_mile_cents,omitempty"`
	FlatCents        Cents  `json:"flat_cents,omitempty"`
	MinimumCents     Cents  `json:"minimum_cents"`
}

// FuelSchedule maps the published diesel price to a surcharge on linehaul.
type FuelSchedule struct {
	Bands []FuelBand `json:"bands"`
}

// FuelBand applies when FromCPG <= price < ToCPG (cents per gallon).
type FuelBand struct {
	FromCPG      int64 `json:"from_cpg"`
	ToCPG        int64 `json:"to_cpg"`
	SurchargeBps int64 `json:"surcharge_bps"` // basis points of linehaul
}

// DetentionRule is the contract's waiting-time clause.
type DetentionRule struct {
	FreeMinutes      int   `json:"free_minutes"`
	RatePerHourCents Cents `json:"rate_per_hour_cents"`
	IncrementMinutes int   `json:"increment_minutes"` // billed in whole increments
	RoundUp          bool  `json:"round_up"`          // partial increments round up instead of down
	CapPerStopCents  Cents `json:"cap_per_stop_cents"`
	// A carrier that arrives more than LateGraceMinutes after its appointment forfeits detention.
	LateGraceMinutes int `json:"late_grace_minutes"`
}

// Validate rejects tariffs that would make rating ambiguous.
func (t Tariff) Validate() error {
	var errs []error
	if t.ID == "" || t.Version < 1 || t.CarrierID == "" {
		errs = append(errs, errors.New("tariff needs id, version >= 1 and carrier_id"))
	}
	seen := map[string]bool{}
	for _, l := range t.Lanes {
		if seen[l.ID] {
			errs = append(errs, fmt.Errorf("lane %q defined twice", l.ID))
		}
		seen[l.ID] = true
		if (l.RatePerMileCents > 0) == (l.FlatCents > 0) {
			errs = append(errs, fmt.Errorf("lane %q needs exactly one of rate_per_mile_cents or flat_cents", l.ID))
		}
	}
	for i, b := range t.Fuel.Bands {
		if b.ToCPG <= b.FromCPG {
			errs = append(errs, fmt.Errorf("fuel band %d is empty", i))
		}
		if i > 0 && b.FromCPG != t.Fuel.Bands[i-1].ToCPG {
			errs = append(errs, fmt.Errorf("fuel band %d does not start where band %d ends", i, i-1))
		}
	}
	d := t.Detention
	if d.RatePerHourCents > 0 && d.IncrementMinutes <= 0 {
		errs = append(errs, errors.New("detention needs increment_minutes > 0"))
	}
	return errors.Join(errs...)
}

// Lane looks up a lane by ID.
func (t Tariff) Lane(id string) (Lane, bool) {
	for _, l := range t.Lanes {
		if l.ID == id {
			return l, true
		}
	}
	return Lane{}, false
}

// LinehaulCents rates the main move: per-mile or flat, never below the lane minimum.
func LinehaulCents(l Lane, miles int) Cents {
	amount := l.FlatCents
	if l.RatePerMileCents > 0 {
		amount = l.RatePerMileCents * Cents(miles)
	}
	return max(amount, l.MinimumCents)
}

// ErrFuelPriceOutOfTable means the published diesel price falls outside every band.
var ErrFuelPriceOutOfTable = errors.New("fuel price outside the tariff's fuel table")

// FuelCents rates the fuel surcharge for a diesel price in cents per gallon.
func FuelCents(s FuelSchedule, linehaul Cents, priceCPG int64) (Cents, FuelBand, error) {
	for _, b := range s.Bands {
		if priceCPG >= b.FromCPG && priceCPG < b.ToCPG {
			return Cents(divRound(int64(linehaul)*b.SurchargeBps, 10_000)), b, nil
		}
	}
	return 0, FuelBand{}, ErrFuelPriceOutOfTable
}

// DetentionCalc explains a detention amount, so every audit finding can show its working.
type DetentionCalc struct {
	ClockStart      time.Time `json:"clock_start"`
	ClockEnd        time.Time `json:"clock_end"`
	DwellMinutes    int       `json:"dwell_minutes"`
	BillableMinutes int       `json:"billable_minutes"`
	Increments      int       `json:"increments"`
	Forfeited       bool      `json:"forfeited"`
	LateMinutes     int       `json:"late_minutes,omitempty"`
	Capped          bool      `json:"capped"`
	AmountCents     Cents     `json:"amount_cents"`
}

// DetentionCents applies the detention clause to one stop.
//
// The clock starts at arrival, or at the appointment if the truck arrived early.
// A truck later than appointment + grace forfeits detention. Waiting beyond the free
// time is billed in whole increments at the hourly rate, up to the per-stop cap.
func DetentionCents(r DetentionRule, appointment, arrival, departure time.Time) DetentionCalc {
	c := DetentionCalc{ClockStart: arrival, ClockEnd: departure}
	if !appointment.IsZero() {
		if late := int(arrival.Sub(appointment) / time.Minute); late > r.LateGraceMinutes {
			c.Forfeited, c.LateMinutes = true, late
			return c
		}
		if arrival.Before(appointment) {
			c.ClockStart = appointment
		}
	}
	if !c.ClockEnd.After(c.ClockStart) || r.IncrementMinutes <= 0 {
		return c
	}
	c.DwellMinutes = int(c.ClockEnd.Sub(c.ClockStart) / time.Minute)
	c.BillableMinutes = max(0, c.DwellMinutes-r.FreeMinutes)
	c.Increments = c.BillableMinutes / r.IncrementMinutes
	if r.RoundUp && c.BillableMinutes%r.IncrementMinutes != 0 {
		c.Increments++
	}
	amount := Cents(divRound(int64(c.Increments)*int64(r.IncrementMinutes)*int64(r.RatePerHourCents), 60))
	if r.CapPerStopCents > 0 && amount > r.CapPerStopCents {
		amount, c.Capped = r.CapPerStopCents, true
	}
	c.AmountCents = amount
	return c
}

// divRound divides non-negative integers, rounding half up.
func divRound(num, den int64) int64 { return (num + den/2) / den }
