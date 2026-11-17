package timeline

import (
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func m(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

var stops = []Stop{{Seq: 1, Type: "pickup", Appointment: m(0)}, {Seq: 2, Type: "delivery", Appointment: m(600)}}

func sample() []Event {
	return []Event{
		{"d1a", DockSystem, 1, Arrived, m(-5)},
		{"d1d", DockSystem, 1, Departed, m(90)},
		{"c1a", CarrierEDI, 1, Arrived, m(-5)},
		{"c1d", CarrierEDI, 1, Departed, m(90)},
		{"e2a1", ELDGeofence, 2, Arrived, m(598)},
		{"e2a2", ELDGeofence, 2, Arrived, m(611)},  // yard move: later re-entry
		{"e2d2", ELDGeofence, 2, Departed, m(601)}, // briefly left the fence
		{"e2d1", ELDGeofence, 2, Departed, m(790)},
		{"c2a", CarrierEDI, 2, Arrived, m(598)},
		{"c2d", CarrierEDI, 2, Departed, m(850)}, // carrier claims an hour longer
		{"x9", ELDGeofence, 9, Arrived, m(1)},    // unknown stop
	}
}

func TestBuildPicksMostTrustedAndWidestWindow(t *testing.T) {
	tl := Build(stops, sample(), 10*time.Minute)
	s1, _ := tl.Stop(1)
	if s1.Arrival.Source != DockSystem || s1.Departure.EventID != "d1d" || len(s1.Conflicts) != 0 {
		t.Fatalf("stop 1: %+v", s1)
	}
	s2, _ := tl.Stop(2)
	if s2.Arrival.EventID != "e2a1" || s2.Departure.EventID != "e2d1" {
		t.Fatalf("stop 2 should use the first ELD arrival and last ELD departure: %+v", s2)
	}
	if len(s2.Conflicts) != 1 || s2.Conflicts[0].Source != CarrierEDI || s2.Conflicts[0].DeltaMinute != 60 {
		t.Fatalf("carrier's later departure should be a +60 min conflict: %+v", s2.Conflicts)
	}
	if !tl.Complete() || len(tl.Ignored) != 1 || tl.Ignored[0] != "x9" {
		t.Fatalf("complete=%v ignored=%v", tl.Complete(), tl.Ignored)
	}
}

func TestBuildIgnoresOrderAndDuplicates(t *testing.T) {
	want := Build(stops, sample(), 10*time.Minute)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 200; i++ {
		evs := sample()
		evs = append(evs, evs[r.IntN(len(evs))], evs[r.IntN(len(evs))])
		r.Shuffle(len(evs), func(i, j int) { evs[i], evs[j] = evs[j], evs[i] })
		if got := Build(stops, evs, 10*time.Minute); !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d changed the timeline:\n got %+v\nwant %+v", i, got, want)
		}
	}
}

func TestIncompleteAndAnomalous(t *testing.T) {
	tl := Build(stops, []Event{{"a", DockSystem, 1, Arrived, m(0)}}, time.Minute)
	if tl.Complete() {
		t.Fatal("missing departures cannot be complete")
	}
	tl = Build(stops[:1], []Event{{"a", DockSystem, 1, Arrived, m(10)}, {"b", DockSystem, 1, Departed, m(5)}}, time.Minute)
	if s, _ := tl.Stop(1); s.Anomaly == "" || s.Complete() {
		t.Fatalf("departure before arrival should be an anomaly: %+v", s)
	}
}
