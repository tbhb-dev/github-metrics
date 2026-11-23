// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
	"github.com/tbhb-dev/github-metrics/internal/ghclient"
	"github.com/tbhb-dev/github-metrics/internal/reconcile"
	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

var errBoom = errors.New("boom")

func runNow() time.Time   { return time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC) }
func firstEnd() time.Time { return time.Date(2026, time.March, 10, 9, 0, 0, 0, time.UTC) }

// walkOf is a clean walk of one page.
func walkOf(items ...reconcile.ClosedPR) reconcile.Walked {
	return reconcile.Walked{Newest: runNow(), Items: items, Pages: 1, Truncated: false, Unchanged: false}
}

type fakeSource struct {
	walks   map[string]reconcile.Walked
	errs    map[string]error
	stops   map[string]time.Time
	listErr error
	repos   []reconcile.Repository
	cut     bool
}

func (f *fakeSource) Repositories(context.Context) ([]reconcile.Repository, bool, error) {
	return f.repos, f.cut, f.listErr
}

func (f *fakeSource) Updated(_ context.Context, repo string, opts reconcile.WalkOptions) (reconcile.Walked, error) {
	f.stops[repo] = opts.Stop

	return f.walks[repo], f.errs[repo]
}

type fakeRecorder struct {
	out recorder.Outcome
	err error
	got []recorder.ClosedPR
}

func (f *fakeRecorder) RecordClosed(_ context.Context, closed recorder.ClosedPR) (recorder.Outcome, error) {
	f.got = append(f.got, closed)

	return f.out, f.err
}

type fakeStore struct {
	marks   map[string]time.Time
	readErr error
	setErr  error
	sets    int
}

func (f *fakeStore) Watermark(repo string) (time.Time, error) { return f.marks[repo], f.readErr }

func (f *fakeStore) SetWatermark(repo string, at time.Time) error {
	f.sets++
	f.marks[repo] = at

	return f.setErr
}

type fakeClock struct{}

func (fakeClock) Now() time.Time { return runNow() }

// fakeEvents answers each events target with a queue of pages, then records the tags cleared.
type eventPage struct{ next, body string }

func one(body string) []eventPage { return []eventPage{{next: "", body: body}} }

type fakeEvents struct {
	pages   map[string][]eventPage
	err     error
	cleared []string
	asked   []string
}

func (f *fakeEvents) Get(_ context.Context, priority budget.Priority, target string) (*ghclient.Response, error) {
	f.asked = append(f.asked, target)

	if f.err != nil || priority != budget.Incremental {
		return nil, errors.Join(f.err, errBoom)
	}

	queue := f.pages[target]
	if len(queue) == 0 {
		return &ghclient.Response{ETag: "", Next: "", Body: []byte("[]"), Status: 200}, nil
	}

	f.pages[target] = queue[1:]

	return &ghclient.Response{ETag: "", Next: queue[0].next, Body: []byte(queue[0].body), Status: 200}, nil
}

func (f *fakeEvents) Set(target, etag string) {
	if etag == "" {
		f.cleared = append(f.cleared, target)
	}
}

type runRig struct {
	source *fakeSource
	rec    *fakeRecorder
	store  *fakeStore
	events *fakeEvents
	runner *reconcile.Runner
}

func newRunRig(t *testing.T, names ...string) *runRig {
	t.Helper()

	rig := &runRig{
		source: &fakeSource{
			walks: map[string]reconcile.Walked{}, errs: map[string]error{}, stops: map[string]time.Time{},
			repos: nil, listErr: nil, cut: false,
		},
		rec:    &fakeRecorder{out: recorder.Outcome{Merged: "", Ended: recorder.ResultRecorded}, err: nil, got: nil},
		store:  &fakeStore{marks: map[string]time.Time{}, readErr: nil, setErr: nil, sets: 0},
		runner: nil,
		events: &fakeEvents{pages: map[string][]eventPage{}, err: nil, cleared: nil, asked: nil},
	}

	for _, name := range names {
		rig.source.repos = append(rig.source.repos, reconcile.Repository{FullName: name, ID: 1})
	}

	base, err := url.Parse("https://api.example.test/")
	if err != nil {
		t.Fatal(err)
	}

	rig.runner = reconcile.NewRunner(reconcile.Deps{
		Source: rig.source, Recorder: rig.rec, Store: rig.store, Clock: fakeClock{},
		Events: rig.events, Tags: rig.events, Base: base,
	})

	return rig
}

func closedItem(repo, node string, number int, merged bool) reconcile.ClosedPR {
	var mergedAt time.Time
	if merged {
		mergedAt = firstEnd()
	}

	return reconcile.ClosedPR{
		CreatedAt: runNow().Add(-5 * time.Hour), MergedAt: mergedAt, ClosedAt: runNow().Add(-time.Hour),
		UpdatedAt: runNow().Add(-time.Hour), NodeID: node, Number: number, Body: "b", Repository: repo, Merged: merged,
	}
}

func events(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, `{"event":"`+pairs[i]+`","created_at":"`+pairs[i+1]+`"}`)
	}

	return "[" + strings.Join(parts, ",") + "]"
}

func TestRunRecordsFirstEndAndAdvancesWatermark(t *testing.T) {
	t.Parallel()

	rig := newRunRig(t, "o/r")
	newest := runNow().Add(-30 * time.Minute)
	rig.source.walks["o/r"] = reconcile.Walked{
		Newest: newest, Pages: 2, Truncated: false, Unchanged: false,
		Items: []reconcile.ClosedPR{closedItem("o/r", "N1", 7, true), closedItem("o/r", "N2", 8, false)},
	}
	// N1 closed, reopened and merged: the first close is not the merge, so it ends as closed.
	rig.events.pages["repos/o/r/issues/7/events?per_page=100"] = one(events(
		"closed", "2026-03-10T09:00:00Z", "reopened", "2026-03-10T09:30:00Z",
		"closed", "2026-03-10T10:00:00Z", "merged", "2026-03-10T10:00:00Z",
	))
	rig.events.pages["repos/o/r/issues/8/events?per_page=100"] = one(events(
		"closed", "2026-03-10T09:00:00Z", "merged", "2026-03-10T09:00:00Z",
	))

	sum, err := rig.runner.Run(t.Context())
	if err != nil || sum.Result != reconcile.ResultOK || sum.Repositories != 1 || sum.Pages != 4 || sum.Recorded != 2 {
		t.Fatalf("sum = %+v, err = %v", sum, err)
	}

	if len(rig.rec.got) != 2 || rig.rec.got[0].EndedState != "closed" || !rig.rec.got[0].EndedAt.Equal(firstEnd()) ||
		!rig.rec.got[0].Merged || rig.rec.got[0].MergedAt.IsZero() || rig.rec.got[1].EndedState != "merged" {
		t.Errorf("recorded = %+v", rig.rec.got)
	}

	if !rig.store.marks["o/r"].Equal(newest) || len(rig.events.cleared) != 2 ||
		rig.events.cleared[0] != "https://api.example.test/repos/o/r/issues/7/events?per_page=100" {
		t.Errorf("mark = %v, cleared = %v", rig.store.marks, rig.events.cleared)
	}
}

func TestRunStopTimeIsLaterOfOverlapAndWindow(t *testing.T) {
	t.Parallel()

	rig := newRunRig(t, "fresh", "recent", "old")
	rig.store.marks["recent"] = runNow().Add(-time.Hour)
	rig.store.marks["old"] = runNow().Add(-72 * time.Hour)

	_, err := rig.runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]time.Time{
		"fresh": runNow().Add(-reconcile.Window), "recent": runNow().Add(-time.Hour - reconcile.Overlap),
		"old": runNow().Add(-reconcile.Window),
	}
	for repo, stop := range want {
		if !rig.source.stops[repo].Equal(stop) {
			t.Errorf("stop of %s = %v, want %v", repo, rig.source.stops[repo], stop)
		}
	}

	if rig.store.sets != 0 {
		t.Errorf("an empty walk moved a watermark %d times", rig.store.sets)
	}
}

func TestRunAmbiguousRecordsMergeOnly(t *testing.T) {
	t.Parallel()

	rig := newRunRig(t, "o/r")
	rig.source.walks["o/r"] = walkOf(
		closedItem("o/r", "none", 1, true),
		closedItem("o/r", "long", 2, true),
		closedItem("o/r", "bad", 3, true),
	)
	// No closed event for the first PR, four pages for the second.
	rig.events.pages["repos/o/r/issues/1/events?per_page=100"] = one(events("labeled", "2026-03-10T09:00:00Z"))
	rig.events.pages["repos/o/r/issues/2/events?per_page=100"] = []eventPage{
		{next: "https://api.example.test/p2", body: events("closed", "2026-03-10T09:00:00Z")},
	}
	rig.events.pages["https://api.example.test/p2"] = []eventPage{{next: "https://api.example.test/p3", body: "[]"}}
	rig.events.pages["https://api.example.test/p3"] = []eventPage{{next: "https://api.example.test/p4", body: "[]"}}
	rig.events.pages["repos/o/r/issues/3/events?per_page=100"] = one("not json")

	sum, err := rig.runner.Run(t.Context())
	if err != nil || sum.Ambiguous != 2 || sum.Result != reconcile.ResultPartial || len(rig.rec.got) != 2 {
		t.Fatalf("sum = %+v, err = %v, recorded = %d", sum, err, len(rig.rec.got))
	}

	for _, closed := range rig.rec.got {
		if !closed.EndedAt.IsZero() || closed.EndedState != "" || !closed.Merged || closed.MergedAt.IsZero() {
			t.Errorf("closed = %+v, want merge only", closed)
		}
	}

	if rig.store.sets != 0 || len(rig.events.asked) != 5 {
		t.Errorf("sets = %d, asked = %v", rig.store.sets, rig.events.asked)
	}
}

func TestRunCountsOutcomes(t *testing.T) {
	t.Parallel()

	var open reconcile.ClosedPR

	rig := newRunRig(t, "o/r")
	rig.rec.out = recorder.Outcome{Merged: recorder.ResultAlreadyCounted, Ended: recorder.ResultTooOld}
	rig.source.walks["o/r"] = walkOf(closedItem("o/r", "N1", 1, true), open)
	rig.events.pages["repos/o/r/issues/1/events?per_page=100"] = one(events("closed", "2026-03-10T09:00:00Z"))

	sum, err := rig.runner.Run(t.Context())
	if err != nil || sum.AlreadyCounted != 1 || sum.TooOld != 1 || sum.Recorded != 0 || len(rig.rec.got) != 1 {
		t.Errorf("sum = %+v, err = %v, recorded = %d", sum, err, len(rig.rec.got))
	}
}

func TestRunWalkOutcomes(t *testing.T) {
	t.Parallel()

	rig := newRunRig(t, "same", "cut")
	rig.store.marks["same"] = runNow().Add(-time.Hour)
	rig.source.walks["same"] = reconcile.Walked{
		Newest:    time.Time{},
		Items:     nil,
		Pages:     1,
		Truncated: false,
		Unchanged: true,
	}
	rig.source.walks["cut"] = reconcile.Walked{
		Newest: time.Time{}, Pages: 50, Truncated: true, Unchanged: false,
		Items: []reconcile.ClosedPR{closedItem("cut", "N1", 1, false)},
	}
	rig.events.pages["repos/cut/issues/1/events?per_page=100"] = one(events("closed", "2026-03-10T09:00:00Z"))

	sum, err := rig.runner.Run(t.Context())
	if err != nil || sum.NotModified != 1 || sum.Truncated != 1 || sum.Pages != 52 || sum.Recorded != 1 ||
		sum.Result != reconcile.ResultPartial || rig.store.sets != 0 {
		t.Errorf("sum = %+v, err = %v, sets = %d", sum, err, rig.store.sets)
	}
}

func TestRunFailureTable(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		err  error
		want reconcile.Result
	}{
		"token source": {fakeWrap(ghclient.ErrTokenSource), reconcile.ResultAuthFailed},
		"401":          {fakeWrap(&ghclient.StatusError{Path: "p", Status: 401}), reconcile.ResultAuthFailed},
		"403":          {fakeWrap(&ghclient.StatusError{Path: "p", Status: 403}), reconcile.ResultPartial},
		"404":          {fakeWrap(&ghclient.StatusError{Path: "p", Status: 404}), reconcile.ResultPartial},
		"gave up":      {fakeWrap(ghclient.ErrGaveUp), reconcile.ResultPartial},
		"deadline":     {fakeWrap(context.DeadlineExceeded), reconcile.ResultPartial},
		"too large":    {fakeWrap(ghclient.ErrBodyTooLarge), reconcile.ResultPartial},
		"decode":       {fakeWrap(errBoom), reconcile.ResultPartial},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRunRig(t, "bad", "good")
			rig.source.errs["bad"] = test.err
			rig.source.walks["good"] = walkOf()

			sum, err := rig.runner.Run(t.Context())
			if err != nil || sum.Result != test.want {
				t.Fatalf("result = %q, err = %v, want %q", sum.Result, err, test.want)
			}

			// An auth failure ends the run. Any other failure skips only that repository.
			advanced := rig.store.sets == 1
			if advanced != (test.want == reconcile.ResultPartial) || (sum.Repositories == 2) != advanced {
				t.Errorf("sets = %d, repositories = %d", rig.store.sets, sum.Repositories)
			}
		})
	}
}

func fakeWrap(err error) error { return errors.Join(errBoom, err) }

func TestRunEventsFailure(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		err  error
		want reconcile.Result
	}{
		"auth":    {ghclient.ErrTokenSource, reconcile.ResultAuthFailed},
		"missing": {&ghclient.StatusError{Path: "p", Status: 404}, reconcile.ResultPartial},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRunRig(t, "o/r")
			rig.events.err = test.err
			rig.source.walks["o/r"] = walkOf(closedItem("o/r", "N1", 1, true))

			sum, err := rig.runner.Run(t.Context())
			if err != nil || sum.Result != test.want || len(rig.rec.got) != 0 || rig.store.sets != 0 {
				t.Errorf("sum = %+v, err = %v, sets = %d", sum, err, rig.store.sets)
			}
		})
	}
}

func TestRunListFailure(t *testing.T) {
	t.Parallel()

	rig := newRunRig(t)
	rig.source.listErr = fakeWrap(ghclient.ErrTokenSource)

	sum, err := rig.runner.Run(t.Context())
	if err != nil || sum.Result != reconcile.ResultAuthFailed {
		t.Errorf("sum = %+v, err = %v", sum, err)
	}

	rig = newRunRig(t, "o/r")
	rig.source.cut = true

	sum, err = rig.runner.Run(t.Context())
	if err != nil || sum.Result != reconcile.ResultPartial || sum.Repositories != 1 {
		t.Errorf("cut list: sum = %+v, err = %v", sum, err)
	}
}

func TestRunStoreErrorsAreReturned(t *testing.T) {
	t.Parallel()

	for name, arrange := range map[string]func(*runRig){
		"read mark": func(r *runRig) { r.store.readErr = errBoom },
		"set mark":  func(r *runRig) { r.store.setErr = errBoom },
		"claim":     func(r *runRig) { r.rec.err = errBoom },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRunRig(t, "o/r", "next")
			rig.source.walks["o/r"] = walkOf(closedItem("o/r", "N1", 1, true))
			rig.events.pages["repos/o/r/issues/1/events?per_page=100"] = one(events("closed", "2026-03-10T09:00:00Z"))
			arrange(rig)

			sum, err := rig.runner.Run(t.Context())
			if !errors.Is(err, errBoom) || sum.Repositories != 1 {
				t.Errorf("sum = %+v, err = %v, want errBoom after one repository", sum, err)
			}
		})
	}
}
