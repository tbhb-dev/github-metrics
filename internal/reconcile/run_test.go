// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
	"github.com/tbhb-dev/github-metrics/internal/ghclient"
	"github.com/tbhb-dev/github-metrics/internal/reconcile"
	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

const (
	firstRepo = "o/first"
	nextRepo  = "o/next"
	lastRepo  = "o/last"

	closedState = "closed"
)

type runClock time.Time

func (clock runClock) Now() time.Time { return time.Time(clock) }

type runFake struct {
	readErr, writeErr    error
	end                  func(reconcile.ClosedPR) (reconcile.End, error)
	walk                 func(string, reconcile.WalkOptions) (reconcile.Walked, error)
	record               func(recorder.ClosedPR) (recorder.Outcome, error)
	watermarks           map[string]time.Time
	repos, walked, wrote []string
	cleared              []string
}

// ended is the first end the fake events reads give: a plain close.
func ended(reconcile.ClosedPR) reconcile.End {
	return reconcile.End{At: time.Date(2026, time.January, 2, 8, 0, 0, 0, time.UTC), State: closedState, Pages: 1}
}

func (fake *runFake) ClearUpdated(repo string) { fake.cleared = append(fake.cleared, repo) }

func (fake *runFake) FirstEnd(_ context.Context, repo string, number int) (reconcile.End, error) {
	item := reconcile.ClosedPR{
		CreatedAt: time.Time{}, MergedAt: time.Time{}, ClosedAt: time.Time{}, UpdatedAt: time.Time{},
		NodeID: "", Body: "", Repository: repo, Number: number, Merged: false,
	}
	if fake.end == nil {
		return ended(item), nil
	}

	return fake.end(item)
}

func (fake *runFake) Repositories(context.Context) ([]reconcile.Repository, bool, error) {
	repos := make([]reconcile.Repository, 0, len(fake.repos))
	for _, name := range fake.repos {
		repos = append(repos, reconcile.Repository{FullName: name, ID: 0})
	}

	return repos, false, nil
}

func (fake *runFake) Updated(_ context.Context, repo string, opts reconcile.WalkOptions) (reconcile.Walked, error) {
	fake.walked = append(fake.walked, repo)

	return fake.walk(repo, opts)
}

func (fake *runFake) RecordClosed(_ context.Context, closed recorder.ClosedPR) (recorder.Outcome, error) {
	return fake.record(closed)
}

func (fake *runFake) Watermark(repo string) (time.Time, error) {
	return fake.watermarks[repo], fake.readErr
}

func (fake *runFake) SetWatermark(repo string, updated time.Time) error {
	fake.wrote = append(fake.wrote, repo)
	if fake.writeErr == nil {
		fake.watermarks[repo] = updated
	}

	return fake.writeErr
}

func TestRunRepairsMerges(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	created, merged, old := now.Add(-6*time.Hour), now.Add(-time.Hour), now.Add(-48*time.Hour)
	results := []recorder.Result{
		recorder.ResultRecorded, recorder.ResultAlreadyCounted, recorder.ResultTooOld, recorder.ResultPreFloor,
	}

	items := make([]reconcile.ClosedPR, 0, len(results)+1)
	for i := range results {
		items = append(items, reconcile.ClosedPR{
			CreatedAt: created, MergedAt: merged, ClosedAt: merged, UpdatedAt: now,
			NodeID: fmt.Sprintf("N%d", i), Body: "", Repository: firstRepo, Number: 0, Merged: true,
		})
	}

	// An item with a merge time and no close time has nothing to record.
	items = append(items, reconcile.ClosedPR{MergedAt: merged}) //nolint:exhaustruct_v5 // A missing close time.
	walks := map[string]reconcile.Walked{
		firstRepo: {Newest: now, Items: items, Pages: 2, Truncated: false, Unchanged: false},
		nextRepo:  {Newest: now, Items: nil, Pages: 1, Truncated: false, Unchanged: true},
		lastRepo:  {Newest: now, Items: nil, Pages: 50, Truncated: true, Unchanged: false},
	}

	var fake runFake

	fake.repos = []string{firstRepo, nextRepo, lastRepo}
	fake.watermarks = map[string]time.Time{firstRepo: merged, lastRepo: old}
	fake.walk = func(repo string, got reconcile.WalkOptions) (reconcile.Walked, error) {
		stop := now.Add(-24 * time.Hour)
		if repo == firstRepo {
			stop = merged.Add(-5 * time.Minute)
		}

		if !got.Stop.Equal(stop) || got.MaxPages != reconcile.DefaultMaxPages || got.Priority != budget.Incremental {
			t.Errorf("walk %s = %+v, want stop %v and incremental bounds", repo, got, stop)
		}

		return walks[repo], nil
	}

	calls := 0
	fake.record = func(got recorder.ClosedPR) (recorder.Outcome, error) {
		if calls >= len(results) || len(fake.wrote) != 0 {
			t.Fatalf("unexpected record %d after watermark writes %v", calls, fake.wrote)
		}

		want := recorder.ClosedPR{
			CreatedAt:  created,
			MergedAt:   merged,
			EndedAt:    ended(items[0]).At,
			Repository: firstRepo,
			NodeID:     items[calls].NodeID,
			Body:       "",
			EndedState: closedState,
			Merged:     true,
			Repair:     true,
		}
		if got != want {
			t.Errorf("record = %+v, want %+v", got, want)
		}

		outcome := recorder.Outcome{Merged: results[calls], Ended: results[(calls+1)%len(results)]}
		calls++

		return outcome, nil
	}

	want := reconcile.RunResult{
		Result: reconcile.RunPartial, Repositories: 3, Pages: 57, NotModified: 1, Truncated: 1,
		Recorded: 1, AlreadyCounted: 1, TooOld: 1, PreFloor: 1, Skipped: 1,
		EndedRecorded: 1, EndedAlreadyCounted: 1, EndedTooOld: 1, EndedPreFloor: 1, Ambiguous: 0,
	}

	got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))
	if err != nil || got != want || calls != len(results) {
		t.Fatalf("result = %+v, err = %v, calls = %d, want %+v and %d calls", got, err, calls, want, len(results))
	}

	if !slices.Equal(fake.wrote, []string{firstRepo}) || !fake.watermarks[firstRepo].Equal(now) {
		t.Errorf("writes = %v, watermarks = %v", fake.wrote, fake.watermarks)
	}
}

type failure struct {
	err    error
	result reconcile.RunStatus
}

// failures lists the read errors of design section 8 and the run result of each.
func failures() []failure {
	decodeErr := json.Unmarshal([]byte("["), new([]reconcile.ClosedPR))

	return []failure{
		{ghclient.ErrTokenSource, reconcile.RunAuthFailed},
		{&ghclient.StatusError{Path: "", Status: http.StatusUnauthorized}, reconcile.RunAuthFailed},
		{&ghclient.StatusError{Path: "", Status: http.StatusForbidden}, reconcile.RunPartial},
		{&ghclient.StatusError{Path: "", Status: http.StatusNotFound}, reconcile.RunPartial},
		{ghclient.ErrGaveUp, reconcile.RunPartial},
		{context.DeadlineExceeded, reconcile.RunPartial},
		{ghclient.ErrBodyTooLarge, reconcile.RunPartial},
		{decodeErr, reconcile.RunPartial},
	}
}

func TestRunWalkFailure(t *testing.T) {
	t.Parallel()

	for _, test := range failures() {
		t.Run(test.err.Error(), func(t *testing.T) {
			t.Parallel()

			var fake runFake

			now := time.Now()
			fake.repos = []string{firstRepo, nextRepo, lastRepo}
			fake.watermarks = make(map[string]time.Time)
			fake.walk = func(repo string, _ reconcile.WalkOptions) (reconcile.Walked, error) {
				walked := reconcile.Walked{Newest: now, Items: nil, Pages: 1, Truncated: false, Unchanged: false}
				if repo == nextRepo {
					return walked, fmt.Errorf("walk: %w", test.err)
				}

				return walked, nil
			}

			got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))

			walked, wrote := fake.repos, []string{firstRepo, lastRepo}
			if test.result == reconcile.RunAuthFailed {
				walked, wrote = walked[:2], wrote[:1]
			}

			if err != nil || got.Result != test.result || got.Repositories != len(walked) || got.Pages != len(walked) ||
				!slices.Equal(fake.walked, walked) || !slices.Equal(fake.wrote, wrote) {
				t.Errorf("result = %+v, err = %v, walked = %v, wrote = %v", got, err, fake.walked, fake.wrote)
			}
		})
	}
}

func TestRunStoreErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		read, claim, write error
		name               string
	}{
		{io.ErrClosedPipe, nil, nil, "read"},
		{nil, io.ErrClosedPipe, nil, "claim"},
		{nil, nil, io.ErrClosedPipe, "write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var fake runFake

			now := time.Now()
			fake.repos = []string{firstRepo, nextRepo}
			fake.watermarks = make(map[string]time.Time)
			fake.readErr, fake.writeErr = test.read, test.write
			fake.walk = func(string, reconcile.WalkOptions) (reconcile.Walked, error) {
				var item reconcile.ClosedPR

				item.ClosedAt, item.MergedAt = now, now
				walked := reconcile.Walked{
					Newest:    now,
					Items:     []reconcile.ClosedPR{item},
					Pages:     1,
					Truncated: false,
					Unchanged: false,
				}

				return walked, nil
			}
			fake.record = func(recorder.ClosedPR) (recorder.Outcome, error) {
				return recorder.Outcome{Merged: recorder.ResultRecorded, Ended: ""}, test.claim
			}

			got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))
			if !errors.Is(err, io.ErrClosedPipe) || got.Result != reconcile.RunPartial || len(fake.walked) > 1 ||
				len(fake.watermarks) != 0 || (test.write == nil && len(fake.wrote) != 0) ||
				(test.claim != nil && !slices.Equal(fake.cleared, []string{firstRepo})) {
				t.Errorf("result = %+v, err = %v, walked = %v, wrote = %v", got, err, fake.walked, fake.wrote)
			}
		})
	}
}

// closedItem is an item that closed, and merged when merged is true.
func closedItem(now time.Time, merged bool) reconcile.ClosedPR {
	item := reconcile.ClosedPR{
		CreatedAt: now.Add(-time.Hour), MergedAt: time.Time{}, ClosedAt: now, UpdatedAt: now,
		NodeID: "N", Body: "", Repository: firstRepo, Number: 7, Merged: false,
	}

	if merged {
		item.MergedAt, item.Merged = now, true
	}

	return item
}

// walkOf returns a walk that always finds the items, with the newest update at now.
func walkOf(now time.Time, items ...reconcile.ClosedPR) func(string, reconcile.WalkOptions) (reconcile.Walked, error) {
	return func(string, reconcile.WalkOptions) (reconcile.Walked, error) {
		return reconcile.Walked{Newest: now, Items: items, Pages: 1, Truncated: false, Unchanged: false}, nil
	}
}

func TestRunRecordsUnmergedCloseAsEnded(t *testing.T) {
	t.Parallel()

	var fake runFake

	now := time.Now()
	item := closedItem(now, false)
	fake.repos = []string{firstRepo}
	fake.watermarks = make(map[string]time.Time)
	fake.walk = walkOf(now, item)
	fake.record = func(got recorder.ClosedPR) (recorder.Outcome, error) {
		want := recorder.ClosedPR{
			CreatedAt: item.CreatedAt, MergedAt: time.Time{}, EndedAt: ended(item).At, Repository: firstRepo,
			NodeID: "N", Body: "", EndedState: closedState, Merged: false, Repair: true,
		}
		if got != want {
			t.Errorf("record = %+v, want %+v", got, want)
		}

		return recorder.Outcome{Merged: "", Ended: recorder.ResultRecorded}, nil
	}

	got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))
	if err != nil || got.EndedRecorded != 1 || got.Recorded != 0 || got.Pages != 2 || got.Result != reconcile.RunOK ||
		!slices.Equal(fake.wrote, []string{firstRepo}) {
		t.Errorf("result = %+v, err = %v, wrote = %v", got, err, fake.wrote)
	}
}

func TestRunAmbiguousEndRecordsMergeOnly(t *testing.T) {
	t.Parallel()

	var fake runFake

	now := time.Now()
	item := closedItem(now, true)
	fake.repos = []string{firstRepo}
	fake.watermarks = make(map[string]time.Time)
	fake.end = func(reconcile.ClosedPR) (reconcile.End, error) {
		return reconcile.End{At: time.Time{}, State: "", Pages: 3}, nil
	}
	fake.walk = walkOf(now, item)
	fake.record = func(got recorder.ClosedPR) (recorder.Outcome, error) {
		if !got.EndedAt.IsZero() || got.EndedState != "" || !got.Merged || !got.MergedAt.Equal(now) {
			t.Errorf("record = %+v, want the merge only", got)
		}

		return recorder.Outcome{Merged: recorder.ResultRecorded, Ended: ""}, nil
	}

	got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))
	if err != nil || got.Ambiguous != 1 || got.Recorded != 1 || got.EndedRecorded != 0 || got.Pages != 4 ||
		got.Result != reconcile.RunOK || !slices.Equal(fake.wrote, []string{firstRepo}) {
		t.Errorf("result = %+v, err = %v, wrote = %v", got, err, fake.wrote)
	}
}

func TestRunEventsFailure(t *testing.T) {
	t.Parallel()

	for _, test := range failures() {
		t.Run(test.err.Error(), func(t *testing.T) {
			t.Parallel()

			var fake runFake

			now := time.Now()
			fake.repos = []string{firstRepo, nextRepo}
			fake.watermarks = make(map[string]time.Time)
			fake.walk = func(repo string, _ reconcile.WalkOptions) (reconcile.Walked, error) {
				item := closedItem(now, true)
				item.Repository = repo

				return reconcile.Walked{
					Newest:    now,
					Items:     []reconcile.ClosedPR{item},
					Pages:     1,
					Truncated: false,
					Unchanged: false,
				}, nil
			}
			fake.end = func(item reconcile.ClosedPR) (reconcile.End, error) {
				end := ended(item)
				if item.Repository == firstRepo {
					return end, nil
				}

				return reconcile.End{At: time.Time{}, State: "", Pages: 1}, fmt.Errorf("read events: %w", test.err)
			}
			fake.record = func(recorder.ClosedPR) (recorder.Outcome, error) {
				return recorder.Outcome{Merged: recorder.ResultRecorded, Ended: recorder.ResultRecorded}, nil
			}

			got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, &fake, runClock(now))
			if err != nil || got.Result != test.result || got.Pages != 4 || got.Recorded != 1 ||
				!slices.Equal(fake.wrote, []string{firstRepo}) || !slices.Equal(fake.cleared, []string{nextRepo}) {
				t.Errorf("result = %+v, err = %v, wrote = %v, cleared = %v", got, err, fake.wrote, fake.cleared)
			}
		})
	}
}
