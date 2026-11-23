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
)

type runClock time.Time

func (clock runClock) Now() time.Time { return time.Time(clock) }

type runFake struct {
	readErr, writeErr    error
	walk                 func(string, reconcile.WalkOptions) (reconcile.Walked, error)
	record               func(recorder.ClosedPR) (recorder.Outcome, error)
	watermarks           map[string]time.Time
	repos, walked, wrote []string
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

	items := make([]reconcile.ClosedPR, 0, len(results)+2)
	for i := range results {
		items = append(items, reconcile.ClosedPR{
			CreatedAt: created, MergedAt: merged, ClosedAt: merged, UpdatedAt: now,
			NodeID: fmt.Sprintf("N%d", i), Body: "", Repository: firstRepo, Merged: true,
		})
	}

	items = append(items,
		reconcile.ClosedPR{ClosedAt: merged}, //nolint:exhaustruct_v5 // A close without a merge.
		reconcile.ClosedPR{MergedAt: merged}, //nolint:exhaustruct_v5 // A missing close time.
	)
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
			CreatedAt: created, MergedAt: merged, EndedAt: time.Time{},
			Repository: firstRepo, NodeID: items[calls].NodeID, Body: "", EndedState: "", Merged: true, Repair: true,
		}
		if got != want {
			t.Errorf("record = %+v, want %+v", got, want)
		}

		outcome := recorder.Outcome{Merged: results[calls], Ended: ""}
		calls++

		return outcome, nil
	}

	want := reconcile.RunResult{
		Result: reconcile.RunPartial, Repositories: 3, Pages: 53, NotModified: 1, Truncated: 1,
		Recorded: 1, AlreadyCounted: 1, TooOld: 1, PreFloor: 1, Skipped: 2,
	}

	got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, runClock(now))
	if err != nil || got != want || calls != len(results) {
		t.Fatalf("result = %+v, err = %v, calls = %d, want %+v and %d calls", got, err, calls, want, len(results))
	}

	if !slices.Equal(fake.wrote, []string{firstRepo}) || !fake.watermarks[firstRepo].Equal(now) {
		t.Errorf("writes = %v, watermarks = %v", fake.wrote, fake.watermarks)
	}
}

func TestRunWalkFailure(t *testing.T) {
	t.Parallel()

	decodeErr := json.Unmarshal([]byte("["), new([]reconcile.ClosedPR))
	for _, test := range []struct {
		err    error
		result reconcile.RunStatus
	}{
		{ghclient.ErrTokenSource, reconcile.RunAuthFailed},
		{&ghclient.StatusError{Path: "", Status: http.StatusUnauthorized}, reconcile.RunAuthFailed},
		{&ghclient.StatusError{Path: "", Status: http.StatusForbidden}, reconcile.RunPartial},
		{&ghclient.StatusError{Path: "", Status: http.StatusNotFound}, reconcile.RunPartial},
		{ghclient.ErrGaveUp, reconcile.RunPartial},
		{context.DeadlineExceeded, reconcile.RunPartial},
		{ghclient.ErrBodyTooLarge, reconcile.RunPartial},
		{decodeErr, reconcile.RunPartial},
	} {
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

			got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, runClock(now))

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

			got, err := reconcile.Run(t.Context(), &fake, &fake, &fake, runClock(now))
			if !errors.Is(err, io.ErrClosedPipe) || got.Result != reconcile.RunPartial || len(fake.walked) > 1 ||
				len(fake.watermarks) != 0 || (test.write == nil && len(fake.wrote) != 0) {
				t.Errorf("result = %+v, err = %v, walked = %v, wrote = %v", got, err, fake.walked, fake.wrote)
			}
		})
	}
}
