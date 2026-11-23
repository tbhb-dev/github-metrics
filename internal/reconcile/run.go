// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
	"github.com/tbhb-dev/github-metrics/internal/ghclient"
	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

const (
	watermarkOverlap = 5 * time.Minute
	repairWindow     = 24 * time.Hour
)

// RepositoryLister supplies the installation's repositories and updated pull requests.
type RepositoryLister interface {
	Repositories(ctx context.Context) ([]Repository, bool, error)
	Updated(ctx context.Context, repo string, opts WalkOptions) (Walked, error)
	// ClearUpdated drops the repository's first-page tag after a failure the
	// walk couldn't see.
	ClearUpdated(repo string)
}

// EndReader reads the first end of a pull request from its issue events.
type EndReader interface {
	FirstEnd(ctx context.Context, repo string, number int) (End, error)
}

// ClosedRecorder claims and records pull request milestones.
type ClosedRecorder interface {
	RecordClosed(ctx context.Context, closed recorder.ClosedPR) (recorder.Outcome, error)
}

// Watermarks stores the latest update seen in each repository's completed walk.
type Watermarks interface {
	Watermark(repository string) (time.Time, error)
	SetWatermark(repository string, updated time.Time) error
}

// Clock supplies the start time of the pass.
type Clock interface {
	Now() time.Time
}

// RunStatus describes how much of the installation the pass covered.
type RunStatus string

// The results of one reconciliation pass.
const (
	RunOK         RunStatus = "ok"
	RunPartial    RunStatus = "partial"
	RunAuthFailed RunStatus = "auth_failed"
)

// RunResult counts attempted repository walks, their fetched pages (pull
// request and event pages) and milestone outcomes. The first four outcome
// counts are for the merge milestone, the next four for the ended milestone.
// Ambiguous counts items whose first end couldn't be read, so only the merge
// was recorded.
type RunResult struct {
	Result                                              RunStatus
	Repositories, Pages, NotModified, Truncated         int
	Recorded, AlreadyCounted, TooOld, PreFloor, Skipped int
	EndedRecorded, EndedAlreadyCounted                  int
	EndedTooOld, EndedPreFloor, Ambiguous               int
}

// Run walks the installation once and repairs merge and ended counts. A
// watermark moves forward only after the whole window has been walked and its
// items processed.
func Run(
	ctx context.Context, lister RepositoryLister, events EndReader, record ClosedRecorder, store Watermarks,
	clock Clock,
) (RunResult, error) {
	var result RunResult

	floor := clock.Now().Add(-repairWindow)

	repos, truncated, err := lister.Repositories(ctx)
	if err != nil {
		result.Result = walkStatus(err)

		return result, nil
	}

	result.Result = RunOK
	if truncated {
		result.Result = RunPartial
	}

	for _, repository := range repos {
		if ctx.Err() != nil {
			result.Result = RunPartial

			break
		}

		err = repairRepository(ctx, lister, events, record, store, repository.FullName, floor, &result)
		if err != nil {
			result.Result = RunPartial

			return result, err
		}

		if result.Result == RunAuthFailed {
			break
		}
	}

	return result, nil
}

func repairRepository(
	ctx context.Context, lister RepositoryLister, events EndReader, record ClosedRecorder, store Watermarks,
	repo string, floor time.Time, result *RunResult,
) error {
	watermark, err := store.Watermark(repo)
	if err != nil {
		return fmt.Errorf("read reconcile watermark: %w", err)
	}

	stop := floor
	if overlap := watermark.Add(-watermarkOverlap); overlap.After(stop) {
		stop = overlap
	}

	result.Repositories++
	opts := WalkOptions{Stop: stop, MaxPages: DefaultMaxPages, Priority: budget.Incremental}
	walked, err := lister.Updated(ctx, repo, opts)
	result.Pages += walked.Pages

	if err != nil {
		result.Result = walkStatus(err)

		return nil
	}

	if walked.Unchanged {
		result.NotModified++

		return nil
	}

	if walked.Truncated {
		result.Truncated++
		result.Result = RunPartial
	}

	// A 304 would hide items this pass couldn't finish, so clear the tag first.
	stopped, err := recordItems(ctx, events, record, walked.Items, result)
	if err != nil || stopped {
		lister.ClearUpdated(repo)

		return err
	}

	if !walked.Truncated && walked.Newest.After(watermark) {
		err = store.SetWatermark(repo, walked.Newest)
		if err != nil {
			return fmt.Errorf("write reconcile watermark: %w", err)
		}
	}

	return nil
}

func walkStatus(err error) RunStatus {
	status, ok := errors.AsType[*ghclient.StatusError](err)
	if errors.Is(err, ghclient.ErrTokenSource) || ok && status.Status == http.StatusUnauthorized {
		return RunAuthFailed
	}

	return RunPartial
}

// recordItems records each item's merge and first end. It reports true when
// an events read failed and the repository's items can't be finished: the run
// is then partial, or failed on auth, and the caller must not advance the
// watermark.
func recordItems(
	ctx context.Context, events EndReader, record ClosedRecorder, items []ClosedPR, result *RunResult,
) (bool, error) {
	for _, item := range items {
		if item.ClosedAt.IsZero() {
			result.Skipped++

			continue
		}

		end, err := events.FirstEnd(ctx, item.Repository, item.Number)
		result.Pages += end.Pages

		if err != nil {
			result.Result = walkStatus(err)

			return true, nil
		}

		if end.At.IsZero() {
			result.Ambiguous++
		}

		outcome, err := record.RecordClosed(ctx, recorder.ClosedPR{
			CreatedAt: item.CreatedAt, MergedAt: item.MergedAt, EndedAt: end.At,
			Repository: item.Repository, NodeID: item.NodeID, Body: item.Body, EndedState: end.State,
			Merged: item.Merged, Repair: true,
		})
		if err != nil {
			return false, fmt.Errorf("record reconciled milestones: %w", err)
		}

		tally(outcome.Merged, &result.Recorded, &result.AlreadyCounted, &result.TooOld, &result.PreFloor)
		tally(
			outcome.Ended,
			&result.EndedRecorded, &result.EndedAlreadyCounted, &result.EndedTooOld, &result.EndedPreFloor,
		)
	}

	return false, nil
}

func tally(outcome recorder.Result, recorded, alreadyCounted, tooOld, preFloor *int) {
	switch outcome {
	case recorder.ResultRecorded:
		*recorded++
	case recorder.ResultAlreadyCounted:
		*alreadyCounted++
	case recorder.ResultTooOld:
		*tooOld++
	case recorder.ResultPreFloor:
		*preFloor++
	}
}
