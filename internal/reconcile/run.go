// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
	"github.com/tbhb-dev/github-metrics/internal/ghclient"
	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

const (
	// Overlap is how far a walk reaches back before a repository's watermark.
	Overlap = 5 * time.Minute
	// Window is how far back a milestone can still count.
	Window = 24 * time.Hour

	// maxEventPages caps the issue events read for one pull request.
	maxEventPages = 3
	eventClosed   = "closed"
)

// Source is the part of Lister a run uses.
type Source interface {
	Repositories(ctx context.Context) ([]Repository, bool, error)
	Updated(ctx context.Context, repo string, opts WalkOptions) (Walked, error)
}

// Recorder is the part of recorder.Recorder a run uses.
type Recorder interface {
	RecordClosed(ctx context.Context, closed recorder.ClosedPR) (recorder.Outcome, error)
}

// Store is the part of the store a run uses.
type Store interface {
	Watermark(repository string) (time.Time, error)
	SetWatermark(repository string, updated time.Time) error
}

// Clock gives the current time.
type Clock interface{ Now() time.Time }

// Result says how a run ended.
type Result string

// The results of one run.
const (
	ResultOK         Result = "ok"
	ResultPartial    Result = "partial"
	ResultAuthFailed Result = "auth_failed"
)

// Summary is what one run did. Pages counts the list and the issue events
// pages read.
type Summary struct {
	Result                                                                                   Result
	Repositories, Pages, NotModified, Recorded, AlreadyCounted, TooOld, Truncated, Ambiguous int
}

// Deps holds what a run reads and writes. Base must be the base URL of Events,
// because the client keys its tags by the full URL, and Tags clears them.
type Deps struct {
	Source   Source
	Recorder Recorder
	Store    Store
	Clock    Clock
	Events   Getter
	Tags     Tags
	Base     *url.URL
}

// Runner makes one pass over the installation at a time.
type Runner struct{ deps Deps }

// NewRunner returns a runner.
func NewRunner(deps Deps) *Runner { return &Runner{deps: deps} }

// Run makes one pass. A repository that fails is skipped and the run ends
// partial. An auth failure ends the run. A store or recorder error comes back
// as the error, with the summary so far.
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	var sum Summary

	sum.Result = ResultOK

	repos, cut, err := r.deps.Source.Repositories(ctx)
	if err != nil {
		r.fail(&sum, err)

		return sum, nil
	}

	if cut {
		sum.Result = ResultPartial
	}

	for _, repo := range repos {
		sum.Repositories++

		err = r.repository(ctx, repo.FullName, &sum)
		if err != nil {
			return sum, err
		}

		if sum.Result == ResultAuthFailed {
			break
		}
	}

	return sum, nil
}

// fail records a read error: auth ends the run, anything else skips the repository.
func (r *Runner) fail(sum *Summary, err error) {
	var status *ghclient.StatusError

	if errors.Is(err, ghclient.ErrTokenSource) || errors.As(err, &status) && status.Status == http.StatusUnauthorized {
		sum.Result = ResultAuthFailed

		return
	}

	sum.Result = ResultPartial
}

func (r *Runner) repository(ctx context.Context, repo string, sum *Summary) error {
	mark, err := r.deps.Store.Watermark(repo)
	if err != nil {
		return fmt.Errorf("read watermark of %s: %w", repo, err)
	}

	now := r.deps.Clock.Now()
	stop := now.Add(-Window)

	if early := mark.Add(-Overlap); early.After(stop) {
		stop = early
	}

	walked, err := r.deps.Source.Updated(
		ctx,
		repo,
		WalkOptions{Stop: stop, MaxPages: DefaultMaxPages, Priority: budget.Incremental},
	)
	if err != nil {
		r.fail(sum, err)

		return nil
	}

	sum.Pages += walked.Pages

	if walked.Unchanged {
		sum.NotModified++

		return nil
	}

	if walked.Truncated {
		sum.Truncated++
		sum.Result = ResultPartial
	}

	for _, item := range walked.Items {
		if item.ClosedAt.IsZero() {
			continue
		}

		ok, itemErr := r.item(ctx, item, sum)
		if itemErr != nil || !ok {
			return itemErr
		}
	}

	if walked.Truncated || !walked.Newest.After(mark) {
		return nil
	}

	err = r.deps.Store.SetWatermark(repo, walked.Newest)
	if err != nil {
		return fmt.Errorf("set watermark of %s: %w", repo, err)
	}

	return nil
}

// item records one closed PR. It returns false when the events read failed,
// which skips the rest of the repository.
func (r *Runner) item(ctx context.Context, item ClosedPR, sum *Summary) (bool, error) {
	closed := recorder.ClosedPR{
		CreatedAt: item.CreatedAt, MergedAt: item.MergedAt, EndedAt: time.Time{},
		Repository: item.Repository, NodeID: item.NodeID, Body: item.Body, EndedState: "", Merged: item.Merged,
	}

	first, merged, err := r.firstEnd(ctx, item, sum)
	if err != nil {
		r.fail(sum, err)

		return false, nil
	}

	if first.IsZero() {
		sum.Ambiguous++
	} else {
		closed.EndedAt, closed.EndedState = first, eventClosed
		if merged {
			closed.EndedState = "merged"
		}
	}

	out, err := r.deps.Recorder.RecordClosed(ctx, closed)
	if err != nil {
		return false, fmt.Errorf("record %s: %w", item.NodeID, err)
	}

	for _, result := range []recorder.Result{out.Merged, out.Ended} {
		switch result {
		case recorder.ResultRecorded:
			sum.Recorded++
		case recorder.ResultAlreadyCounted:
			sum.AlreadyCounted++
		case recorder.ResultTooOld:
			sum.TooOld++
		}
	}

	return true, nil
}

// issueEvent is the part of an issue event the first-end rule reads.
type issueEvent struct {
	CreatedAt time.Time `json:"created_at"` //nolint:tagliatelle // GitHub field name.
	Event     string    `json:"event"`
}

// firstEnd reads the issue events and returns the time of the earliest closed
// event, and whether a merged event shares it. The time is zero when the
// events run past the page cap or hold no closed event.
func (r *Runner) firstEnd(ctx context.Context, item ClosedPR, sum *Summary) (time.Time, bool, error) {
	var all []issueEvent

	target := fmt.Sprintf("repos/%s/issues/%d/events?per_page=100", item.Repository, item.Number)

	for page := 1; ; page++ {
		resp, err := r.events(ctx, target)
		if err != nil {
			return time.Time{}, false, err
		}

		sum.Pages++

		var events []issueEvent

		err = json.Unmarshal(resp.Body, &events)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("decode events: %w", err)
		}

		all = append(all, events...)

		if resp.Next == "" {
			break
		}

		if page >= maxEventPages {
			return time.Time{}, false, nil
		}

		target = resp.Next
	}

	first, merged := settle(all)

	return first, merged, nil
}

// events reads one page. A tag would answer 304 with no body when the same PR
// is read twice, so it clears the tag first.
func (r *Runner) events(ctx context.Context, target string) (*ghclient.Response, error) {
	ref, err := url.Parse(target)
	if err == nil {
		r.deps.Tags.Set(r.deps.Base.ResolveReference(ref).String(), "")
	}

	resp, err := r.deps.Events.Get(ctx, budget.Incremental, target)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}

	return resp, nil
}

// settle returns the earliest closed time, and whether a merged event shares it.
func settle(all []issueEvent) (time.Time, bool) {
	var first time.Time

	for _, event := range all {
		if event.Event == eventClosed && (first.IsZero() || event.CreatedAt.Before(first)) {
			first = event.CreatedAt
		}
	}

	for _, event := range all {
		if event.Event == "merged" && !first.IsZero() && event.CreatedAt.Equal(first) {
			return first, true
		}
	}

	return first, false
}
