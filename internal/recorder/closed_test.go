// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package recorder_test

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

const (
	nodeID      = "PR_1"
	mergedState = "merged"
)

var errClaim = errors.New("claim failed")

func storeCreated() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }

func clockNow() time.Time { return time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC) }

type fakeLedger struct {
	created  time.Time
	claimErr error
	onClaim  func()
	claimed  map[string]bool
	claims   []string
}

func newLedger(adjust func(*fakeLedger)) *fakeLedger {
	var ledger fakeLedger

	ledger.created, ledger.claimed = storeCreated(), map[string]bool{}

	if adjust != nil {
		adjust(&ledger)
	}

	return &ledger
}

func (f *fakeLedger) Created() time.Time { return f.created }

func (f *fakeLedger) ClaimMilestone(key string) (bool, error) {
	if f.onClaim != nil {
		f.onClaim()
	}

	if f.claimErr != nil {
		return false, f.claimErr
	}

	f.claims = append(f.claims, key)

	if f.claimed[key] {
		return false, nil
	}

	f.claimed[key] = true

	return true, nil
}

// closedPR is merged an hour before the fake clock, after an hour open.
func closedPR(adjust func(*recorder.ClosedPR)) recorder.ClosedPR {
	end := clockNow().Add(-time.Hour)

	var closed recorder.ClosedPR

	closed.Repository, closed.NodeID, closed.CreatedAt = repositoryName, nodeID, end.Add(-time.Hour)
	closed.MergedAt, closed.EndedAt, closed.EndedState, closed.Merged = end, end, mergedState, true

	if adjust != nil {
		adjust(&closed)
	}

	return closed
}

// observations returns the merge count and the cycle time states recorded.
func observations(t *testing.T, reader *sdkmetric.ManualReader) (int64, []string) {
	t.Helper()

	var (
		data   metricdata.ResourceMetrics
		merges int64
		states []string
	)

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if sum, ok := measurement.Data.(metricdata.Sum[int64]); ok && measurement.Name == "github.pr.merges" {
				for _, point := range sum.DataPoints {
					merges += point.Value
				}
			}

			if hist, ok := measurement.Data.(metricdata.Histogram[float64]); ok {
				states = append(states, cycleStates(hist)...)
			}
		}
	}

	return merges, states
}

func cycleStates(hist metricdata.Histogram[float64]) []string {
	var states []string

	for _, point := range hist.DataPoints {
		state, _ := point.Attributes.Value("state")
		for range point.Count {
			states = append(states, state.AsString())
		}
	}

	return states
}

func TestRecordClosed(t *testing.T) {
	t.Parallel()

	recorded := recorder.Outcome{Merged: recorder.ResultRecorded, Ended: recorder.ResultRecorded}
	tooOld := recorder.Outcome{Merged: recorder.ResultTooOld, Ended: recorder.ResultTooOld}

	for _, test := range []struct {
		name       string
		adjust     func(*recorder.ClosedPR)
		prepare    func(*fakeLedger)
		want       recorder.Outcome
		wantMerges int64
		wantCycles int
	}{
		{"claim then recorded", nil, nil, recorded, 1, 1},
		{"too old by age", func(closed *recorder.ClosedPR) {
			old := clockNow().Add(-25 * time.Hour)
			closed.CreatedAt, closed.MergedAt, closed.EndedAt = old.Add(-time.Hour), old, old
		}, nil, tooOld, 0, 0},
		{"too old by creation time", nil, func(l *fakeLedger) {
			l.created = clockNow().Add(-30 * time.Minute)
		}, tooOld, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ledger := newLedger(test.prepare)
			consumer, reader := newRecorderWith(t, ledger)

			got, err := consumer.RecordClosed(t.Context(), closedPR(test.adjust))
			if err != nil {
				t.Fatal(err)
			}

			// Every case claims both milestones, with or without an observation.
			merges, cycles := observations(t, reader)
			if got != test.want || len(ledger.claims) != 2 || merges != test.wantMerges ||
				len(cycles) != test.wantCycles {
				t.Fatalf("outcome %v, claims %d, merges %d, cycles %d; want %v, 2, %d, %d",
					got, len(ledger.claims), merges, len(cycles), test.want, test.wantMerges, test.wantCycles)
			}
		})
	}
}

func TestRecordClosedClaimError(t *testing.T) {
	t.Parallel()

	consumer, reader := newRecorderWith(t, newLedger(func(l *fakeLedger) { l.claimErr = errClaim }))

	_, err := consumer.RecordClosed(t.Context(), closedPR(nil))
	if !errors.Is(err, errClaim) {
		t.Fatalf("error = %v, want %v", err, errClaim)
	}

	merges, cycles := observations(t, reader)
	if merges != 0 || len(cycles) != 0 {
		t.Fatalf("merges %d, cycles %d after a claim error; want none", merges, len(cycles))
	}
}

func TestRecordClosedFirstEndKeepsItsState(t *testing.T) {
	t.Parallel()

	consumer, reader := newRecorderWith(t, newLedger(nil))
	closedAt := clockNow().Add(-2 * time.Hour)
	mergedAt := clockNow().Add(-time.Hour)

	first, err := consumer.RecordClosed(t.Context(), closedPR(func(pr *recorder.ClosedPR) {
		pr.Merged, pr.MergedAt, pr.EndedAt, pr.EndedState = false, time.Time{}, closedAt, closedState
	}))
	if err != nil || first.Ended != recorder.ResultRecorded || first.Merged != "" {
		t.Fatalf("first close = %v, %v", first, err)
	}

	// The merge delivery after a reopen carries the merge time and its own end.
	second, err := consumer.RecordClosed(t.Context(), closedPR(func(pr *recorder.ClosedPR) { pr.EndedAt = mergedAt }))
	if err != nil || second.Merged != recorder.ResultRecorded || second.Ended != recorder.ResultAlreadyCounted {
		t.Fatalf("merge after reopen = %v, %v", second, err)
	}

	merges, cycles := observations(t, reader)
	if merges != 1 || len(cycles) != 1 || cycles[0] != closedState {
		t.Fatalf("merges %d, cycle states %v; want 1 and [closed]", merges, cycles)
	}
}

func TestRecordReopened(t *testing.T) {
	t.Parallel()

	const (
		fresh = "2026-11-15T12:00:00Z"
		first = `,"closed_at":"2026-11-15T12:30:00Z"`
		merge = `,"merged":true,"merged_at":"2026-11-15T13:00:00Z","closed_at":"2026-11-15T13:00:00Z"`
	)

	reopen := [2]string{"reopened", ""}
	firstClose := [2]string{closedState, first}
	mergeClose := [2]string{closedState, merge}

	for _, test := range []struct {
		name       string
		created    string
		steps      [][2]string // action and the rest of the pull_request body
		wantCycles int
	}{
		// An earlier version may have counted this first close, so the reopen blocks a second count.
		{"created before the store", "2026-10-20T12:00:00Z", [][2]string{reopen, mergeClose}, 0},
		// This version counts the first close in any delivery order.
		{"reopen before the first close", fresh, [][2]string{reopen, firstClose, mergeClose}, 1},
		{"first close before the reopen", fresh, [][2]string{firstClose, reopen, mergeClose}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			consumer, reader := newRecorderWith(t, newLedger(nil))

			for _, step := range test.steps {
				body := `{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"` + step[0] +
					`","pull_request":{"node_id":"PR_1","created_at":"` + test.created + `"` + step[1] + `}}`

				err := consumer.Record(t.Context(), pullRequestEvent, []byte(body))
				if err != nil {
					t.Fatal(err)
				}
			}

			merges, cycles := observations(t, reader)
			if merges != 1 || len(cycles) != test.wantCycles {
				t.Fatalf("merges %d, cycle states %v; want 1 merge and %d cycle times", merges, cycles, test.wantCycles)
			}
		})
	}
}

func TestRecordClaimsBeforeAnyAdd(t *testing.T) {
	t.Parallel()

	ledger := newLedger(nil)
	consumer, reader := newRecorderWith(t, ledger)

	first := true

	ledger.onClaim = func() {
		counters := collectCounters(t, reader)
		if _, seen := counters["github.events"]; seen && first {
			t.Error("github.events counted before the claim")
		}

		if _, seen := counters["github.pr.merges"]; seen && first {
			t.Error("github.pr.merges counted before the claim")
		}

		first = false
	}

	body := `{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"closed","pull_request":{` +
		`"node_id":"PR_1","merged":true,"created_at":"2026-11-15T12:00:00Z",` +
		`"merged_at":"2026-11-15T13:00:00Z","closed_at":"2026-11-15T13:00:00Z"}}`

	err := consumer.Record(t.Context(), pullRequestEvent, []byte(body))
	if err != nil || len(ledger.claims) != 2 {
		t.Fatalf("Record error %v, claims %v; want two claims", err, ledger.claims)
	}

	ledger.claimErr = errClaim

	err = consumer.Record(t.Context(), pullRequestEvent, []byte(body))
	if !errors.Is(err, errClaim) {
		t.Fatalf("Record error = %v, want %v", err, errClaim)
	}

	if counters := collectCounters(t, reader); counters["github.events"]["action=closed,event=pull_request,"+
		"repository=tbhb-dev/github-metrics"] != 1 {
		t.Fatalf("a failed claim counted the delivery: %v", counters["github.events"])
	}
}

func TestNewWithoutLedger(t *testing.T) {
	t.Parallel()

	_, err := recorder.New(sdkmetric.NewMeterProvider().Meter("test"), nil)
	if !errors.Is(err, recorder.ErrNoLedger) {
		t.Fatalf("New error = %v, want %v", err, recorder.ErrNoLedger)
	}
}
