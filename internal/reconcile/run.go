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

// RunResult counts attempted repository walks, their fetched pages and merge outcomes.
type RunResult struct {
	Result                                              RunStatus
	Repositories, Pages, NotModified, Truncated         int
	Recorded, AlreadyCounted, TooOld, PreFloor, Skipped int
}

// Run walks the installation once and repairs merge counts. A watermark moves
// forward only after the whole window has been walked and its items processed.
func Run(ctx context.Context, lister RepositoryLister, record ClosedRecorder, store Watermarks, clock Clock) (
	RunResult, error,
) {
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

		err = repairRepository(ctx, lister, record, store, repository.FullName, floor, &result)
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
	ctx context.Context, lister RepositoryLister, record ClosedRecorder, store Watermarks,
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

	err = recordMerges(ctx, record, walked.Items, result)
	if err != nil {
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

func recordMerges(ctx context.Context, record ClosedRecorder, items []ClosedPR, result *RunResult) error {
	for _, item := range items {
		if item.ClosedAt.IsZero() || item.MergedAt.IsZero() {
			result.Skipped++

			continue
		}

		outcome, err := record.RecordClosed(ctx, recorder.ClosedPR{
			CreatedAt: item.CreatedAt, MergedAt: item.MergedAt, EndedAt: time.Time{},
			Repository: item.Repository, NodeID: item.NodeID, Body: "", EndedState: "",
			Merged: item.Merged, Repair: true,
		})
		if err != nil {
			return fmt.Errorf("record reconciled merge: %w", err)
		}

		switch outcome.Merged {
		case recorder.ResultRecorded:
			result.Recorded++
		case recorder.ResultAlreadyCounted:
			result.AlreadyCounted++
		case recorder.ResultTooOld:
			result.TooOld++
		case recorder.ResultPreFloor:
			result.PreFloor++
		}
	}

	return nil
}
