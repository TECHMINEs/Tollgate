// Package timeline rebuilds what physically happened at each stop of a shipment from
// stop events reported by several sources that disagree, repeat themselves and arrive
// in any order.
//
// Build is a pure function of the *set* of events: duplicates and delivery order never
// change its result. That property is what lets the auditor be replayed safely.
package timeline

import (
	"fmt"
	"slices"
	"time"
)

// Source is who reported a stop event.
type Source string

const (
	DockSystem  Source = "dock_system"  // the facility's own yard/dock software
	ELDGeofence Source = "eld_geofence" // truck telematics crossing the facility geofence
	CarrierEDI  Source = "carrier_edi"  // the carrier's self-reported status (EDI 214)
)

// Trust ranks sources. The carrier is the party sending the bill, so its own
// timestamps rank lowest.
func Trust(s Source) int {
	switch s {
	case DockSystem:
		return 3
	case ELDGeofence:
		return 2
	case CarrierEDI:
		return 1
	}
	return 0
}

// Independent reports whether a source is independent of the billing carrier.
func Independent(s Source) bool { return s == DockSystem || s == ELDGeofence }

// Kind is the stop transition an event reports.
type Kind string

const (
	Arrived  Kind = "arrived"
	Departed Kind = "departed"
)

// Stop is a planned stop on a shipment.
type Stop struct {
	Seq         int       `json:"seq"`
	Facility    string    `json:"facility"`
	Type        string    `json:"type"` // pickup | delivery
	Appointment time.Time `json:"appointment"`
}

// Event is one reported stop transition.
type Event struct {
	EventID string    `json:"event_id"`
	Source  Source    `json:"source"`
	StopSeq int       `json:"stop_seq"`
	Kind    Kind      `json:"kind"`
	At      time.Time `json:"at"`
}

// Observation is the timestamp chosen for one transition and where it came from.
type Observation struct {
	At      time.Time `json:"at"`
	Source  Source    `json:"source"`
	EventID string    `json:"event_id"`
}

// Known reports whether any source reported this transition.
func (o *Observation) Known() bool { return o != nil }

// Conflict records a lower-trust source disagreeing with the chosen observation.
type Conflict struct {
	Kind        Kind   `json:"kind"`
	Source      Source `json:"source"`
	DeltaMinute int    `json:"delta_minutes"` // other source minus chosen source
}

// StopTimeline is the reconstructed history of one stop.
type StopTimeline struct {
	Stop      Stop         `json:"stop"`
	Arrival   *Observation `json:"arrival,omitempty"`
	Departure *Observation `json:"departure,omitempty"`
	Conflicts []Conflict   `json:"conflicts,omitempty"`
	Anomaly   string       `json:"anomaly,omitempty"`
}

// Complete reports whether both transitions are known and consistent.
func (s StopTimeline) Complete() bool {
	return s.Arrival.Known() && s.Departure.Known() && s.Anomaly == ""
}

// Timeline is the reconstructed history of a shipment.
type Timeline struct {
	Stops    []StopTimeline `json:"stops"`
	EventIDs []string       `json:"event_ids"` // every event that contributed, sorted
	Ignored  []string       `json:"ignored,omitempty"`
}

// Complete reports whether every stop is complete.
func (t Timeline) Complete() bool {
	if len(t.Stops) == 0 {
		return false
	}
	for _, s := range t.Stops {
		if !s.Complete() {
			return false
		}
	}
	return true
}

// Stop returns the timeline for a stop sequence number.
func (t Timeline) Stop(seq int) (StopTimeline, bool) {
	for _, s := range t.Stops {
		if s.Stop.Seq == seq {
			return s, true
		}
	}
	return StopTimeline{}, false
}

// Build reconstructs a timeline.
//
// Within one source, a stop's arrival is the earliest report and its departure the
// latest (a truck can cross a geofence several times in the yard). Across sources the
// most trusted source wins; others that differ by more than tolerance are recorded as
// conflicts.
func Build(stops []Stop, events []Event, tolerance time.Duration) Timeline {
	plan := map[int]Stop{}
	for _, s := range stops {
		plan[s.Seq] = s
	}

	// Canonical order first, so every later tie-break is deterministic.
	evs := slices.Clone(events)
	slices.SortFunc(evs, func(a, b Event) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		if a.EventID < b.EventID {
			return -1
		} else if a.EventID > b.EventID {
			return 1
		}
		return 0
	})

	type key struct {
		seq    int
		kind   Kind
		source Source
	}
	best := map[key]Observation{}
	seen := map[string]bool{}
	var t Timeline
	for _, e := range evs {
		if seen[e.EventID] {
			continue
		}
		seen[e.EventID] = true
		if _, ok := plan[e.StopSeq]; !ok || Trust(e.Source) == 0 || (e.Kind != Arrived && e.Kind != Departed) {
			t.Ignored = append(t.Ignored, e.EventID)
			continue
		}
		t.EventIDs = append(t.EventIDs, e.EventID)
		k := key{e.StopSeq, e.Kind, e.Source}
		cur, ok := best[k]
		obs := Observation{At: e.At, Source: e.Source, EventID: e.EventID}
		switch {
		case !ok:
			best[k] = obs
		case e.Kind == Departed && e.At.After(cur.At):
			best[k] = obs // latest departure; events are sorted, so ties keep the lower ID
		}
	}
	slices.Sort(t.EventIDs)
	slices.Sort(t.Ignored)

	sources := []Source{DockSystem, ELDGeofence, CarrierEDI}
	ordered := slices.Clone(stops)
	slices.SortFunc(ordered, func(a, b Stop) int { return a.Seq - b.Seq })
	for _, s := range ordered {
		st := StopTimeline{Stop: s}
		for _, kind := range []Kind{Arrived, Departed} {
			var chosen *Observation
			for _, src := range sources {
				if o, ok := best[key{s.Seq, kind, src}]; ok && chosen == nil {
					o := o
					chosen = &o
				}
			}
			if chosen == nil {
				continue
			}
			for _, src := range sources {
				o, ok := best[key{s.Seq, kind, src}]
				if !ok || src == chosen.Source {
					continue
				}
				if d := o.At.Sub(chosen.At); d > tolerance || d < -tolerance {
					st.Conflicts = append(st.Conflicts, Conflict{Kind: kind, Source: src, DeltaMinute: int(d / time.Minute)})
				}
			}
			if kind == Arrived {
				st.Arrival = chosen
			} else {
				st.Departure = chosen
			}
		}
		if st.Arrival.Known() && st.Departure.Known() && st.Departure.At.Before(st.Arrival.At) {
			st.Anomaly = fmt.Sprintf("departure (%s) precedes arrival (%s)", st.Departure.Source, st.Arrival.Source)
		}
		t.Stops = append(t.Stops, st)
	}
	return t
}
