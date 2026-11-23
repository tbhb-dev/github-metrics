// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package recorder counts queued GitHub deliveries as metrics.
package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// maxAge is how old a milestone may be and still count, as appendix A step 5 says.
const maxAge = 24 * time.Hour

// ErrNoLedger means New got no ledger to claim milestones in.
var ErrNoLedger = errors.New("recorder needs a ledger")

// Ledger holds what the eligibility rule reads and the one claim per milestone.
// The store satisfies it.
type Ledger interface {
	// Created returns the time of the store's first open.
	Created() time.Time
	// RepairFloor returns the time before which a repair counts nothing.
	RepairFloor() (time.Time, error)
	// ClaimMilestone returns true and keeps the key when it was absent.
	ClaimMilestone(key string) (bool, error)
}

// Result says what RecordClosed did with one milestone.
type Result string

// The results of one milestone.
const (
	ResultTooOld         Result = "too_old"
	ResultPreFloor       Result = "pre_floor"
	ResultAlreadyCounted Result = "already_counted"
	ResultRecorded       Result = "recorded"
)

// Outcome holds the result for each milestone of a closed PR. A milestone the
// PR doesn't have, or has no valid time for, keeps an empty result.
type Outcome struct{ Merged, Ended Result }

// ClosedPR is a closed pull request from a webhook body or a REST list item.
// The first end's time and state travel together, apart from the merge.
type ClosedPR struct {
	CreatedAt, MergedAt, EndedAt time.Time
	Repository, NodeID, Body     string
	// EndedState is "merged" or "closed" and picks the cycle time series.
	EndedState string
	Merged     bool
	// Repair is true on the reconciler path, which also honors the repair floor.
	Repair bool
}

// Recorder counts events without recording webhook text in attributes.
type Recorder struct {
	events, merges, conclusions, dropped metric.Int64Counter
	cycleTime                            metric.Float64Histogram
	mainRed                              metric.Int64Gauge
	ledger                               Ledger
	now                                  func() time.Time
}

type checkRun struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	CheckSuite struct {
		HeadBranch string `json:"head_branch"` //nolint:tagliatelle // GitHub uses this field name.
	} `json:"check_suite"` //nolint:tagliatelle // GitHub uses this field name.
}

type pullRequest struct {
	Body      string `json:"body"`
	NodeID    string `json:"node_id"`    //nolint:tagliatelle // GitHub uses this field name.
	CreatedAt string `json:"created_at"` //nolint:tagliatelle // GitHub uses this field name.
	MergedAt  string `json:"merged_at"`  //nolint:tagliatelle // GitHub uses this field name.
	ClosedAt  string `json:"closed_at"`  //nolint:tagliatelle // GitHub uses this field name.
	Merged    bool   `json:"merged"`
}

// New creates the instruments used to record deliveries. The ledger claims
// each milestone before it counts.
//
//nolint:mnd // The histogram has fixed cycle time buckets in seconds.
func New(meter metric.Meter, ledger Ledger) (*Recorder, error) {
	if ledger == nil {
		return nil, ErrNoLedger
	}

	var recorder Recorder

	recorder.ledger, recorder.now = ledger, time.Now

	for _, counter := range []struct {
		target *metric.Int64Counter
		name   string
	}{
		{&recorder.events, "github.events"},
		{&recorder.merges, "github.pr.merges"},
		{&recorder.conclusions, "github.check.conclusions"},
		{&recorder.dropped, "github.events.dropped"},
	} {
		instrument, err := meter.Int64Counter(counter.name)
		if err != nil {
			return nil, fmt.Errorf("create %s counter: %w", counter.name, err)
		}

		*counter.target = instrument
	}

	for _, reason := range []string{"empty_event", "bad_body", "no_node_id"} {
		recorder.dropped.Add(context.Background(), 0,
			metric.WithAttributes(attribute.String("reason", reason)))
	}

	var err error

	recorder.cycleTime, err = meter.Float64Histogram("github.pr.cycle_time", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			60, 300, 600, 900, 1800, 3600, 7200, 14400, 28800,
			86400, 172800, 259200, 345600, 432000, 518400, 604800, 1209600, 2592000,
		))
	if err != nil {
		return nil, fmt.Errorf("create github.pr.cycle_time histogram: %w", err)
	}

	recorder.mainRed, err = meter.Int64Gauge("github.main.red")
	if err != nil {
		return nil, fmt.Errorf("create github.main.red gauge: %w", err)
	}

	return &recorder, nil
}

// Record counts a delivery or a drop before allowing its removal from the queue.
func (r *Recorder) Record(ctx context.Context, event string, body []byte) error {
	if event == "" {
		r.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "empty_event")))

		return nil
	}

	var delivery struct {
		Repository struct {
			FullName      string `json:"full_name"`      //nolint:tagliatelle // GitHub uses this field name.
			DefaultBranch string `json:"default_branch"` //nolint:tagliatelle // GitHub uses this field name.
		} `json:"repository"`
		Action      string      `json:"action"`
		CheckRun    checkRun    `json:"check_run"`    //nolint:tagliatelle // GitHub uses this field name.
		PullRequest pullRequest `json:"pull_request"` //nolint:tagliatelle // GitHub uses this field name.
	}

	err := json.Unmarshal(body, &delivery)
	if err != nil {
		r.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "bad_body")))

		return nil //nolint:nilerr // Bad data is counted as a drop and may leave the queue.
	}

	repository := delivery.Repository.FullName
	if repository == "" {
		repository = "unknown"
	}

	// Claim before any Add, so a claim error leaves the retried entry uncounted.
	if event == "pull_request" && delivery.Action == "closed" {
		if delivery.PullRequest.NodeID == "" {
			r.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "no_node_id")))

			return nil
		}

		_, err = r.RecordClosed(ctx, closedFromWebhook(repository, delivery.PullRequest))
		if err != nil {
			return err
		}
	}

	repo := attribute.String("repository", repository)
	r.merges.Add(ctx, 0, metric.WithAttributes(repo))

	eventAttrs := metric.WithAttributes(repo,
		attribute.String("event", event), attribute.String("action", delivery.Action))
	r.events.Add(ctx, 0, eventAttrs)
	r.events.Add(ctx, 1, eventAttrs)

	if event == "check_run" {
		r.recordCheckRun(ctx, repo, delivery.Action, delivery.Repository.DefaultBranch, delivery.CheckRun)
	}

	return nil
}

// RecordClosed counts the merge and the first end of a closed PR once each,
// across the webhook and repair paths. A claim error records nothing for that
// milestone and returns the error, so the caller retries.
func (r *Recorder) RecordClosed(ctx context.Context, closed ClosedPR) (Outcome, error) {
	var (
		out Outcome
		err error
	)

	repo := attribute.String("repository", closed.Repository)

	if closed.Merged && !closed.MergedAt.IsZero() {
		out.Merged, err = r.milestone(closed, "merged", closed.MergedAt, func() {
			r.merges.Add(ctx, 0, metric.WithAttributes(repo))
			r.merges.Add(ctx, 1, metric.WithAttributes(repo))
		})
		if err != nil {
			return out, err
		}
	}

	if !closed.EndedAt.IsZero() && !closed.CreatedAt.IsZero() {
		out.Ended, err = r.milestone(closed, "ended", closed.EndedAt, func() { r.recordCycleTime(ctx, repo, closed) })
	}

	return out, err
}

func (r *Recorder) recordCheckRun(
	ctx context.Context, repo attribute.KeyValue, action, defaultBranch string, run checkRun,
) {
	const conclusion = "conclusion"

	checkName := attribute.String("check_name", run.Name)

	for _, known := range []string{
		"success", "failure", "cancelled", "skipped", "neutral",
		"timed_out", "action_required", "stale", "startup_failure",
	} {
		r.conclusions.Add(ctx, 0, metric.WithAttributes(repo, checkName,
			attribute.String(conclusion, known)))
	}

	if action != "completed" {
		return
	}

	r.conclusions.Add(ctx, 1, metric.WithAttributes(repo, checkName,
		attribute.String(conclusion, run.Conclusion)))

	if run.Name != conclusion || run.Conclusion == "" ||
		defaultBranch == "" || run.CheckSuite.HeadBranch != defaultBranch {
		return
	}

	var red int64
	if run.Conclusion == "failure" || run.Conclusion == "timed_out" {
		red = 1
	}

	r.mainRed.Record(ctx, red, metric.WithAttributes(repo))
}

func closedFromWebhook(repository string, request pullRequest) ClosedPR {
	var closed ClosedPR

	closed.Repository, closed.NodeID, closed.Body = repository, request.NodeID, request.Body
	closed.CreatedAt, closed.MergedAt = parseTime(request.CreatedAt), parseTime(request.MergedAt)
	closed.EndedAt, closed.EndedState, closed.Merged = parseTime(request.ClosedAt), "closed", request.Merged

	if request.Merged {
		closed.EndedState = "merged"
	}

	return closed
}

func parseTime(text string) time.Time {
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}

	return parsed
}

// milestone applies the eligibility rule to one milestone time, claims it, and
// calls record only after a true claim.
func (r *Recorder) milestone(closed ClosedPR, name string, when time.Time, record func()) (Result, error) {
	key := closed.NodeID + "\x00" + name
	result := ResultTooOld
	skip := r.now().Sub(when) >= maxAge || !when.After(r.ledger.Created())

	if !skip && closed.Repair {
		floor, err := r.ledger.RepairFloor()
		if err != nil {
			return "", fmt.Errorf("read repair floor: %w", err)
		}

		if !when.After(floor) {
			result, skip = ResultPreFloor, true
		}
	}

	claimed, err := r.ledger.ClaimMilestone(key)
	if err != nil {
		return "", fmt.Errorf("claim %s milestone: %w", name, err)
	}

	switch {
	case skip:
		return result, nil
	case !claimed:
		return ResultAlreadyCounted, nil
	}

	record()

	return ResultRecorded, nil
}

func (r *Recorder) recordCycleTime(ctx context.Context, repo attribute.KeyValue, closed ClosedPR) {
	start, source := cycleStart(closed.CreatedAt, closed.Body)
	if start.IsZero() || closed.EndedAt.Before(start) {
		return
	}

	r.cycleTime.Record(ctx, closed.EndedAt.Sub(start).Seconds(), metric.WithAttributes(repo,
		attribute.String("state", closed.EndedState), attribute.String("start_source", source)))
}

func cycleStart(start time.Time, body string) (time.Time, string) {
	// pr_created is interim: the owner's rule starts at the earliest commit time.
	// That needs the REST commits call in a later change, so interim values read low.
	source := "pr_created"
	startedAt := regexp.MustCompile(`\bstarted_at[:=]\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`)

	for line := range strings.Lines(body) {
		if !strings.HasPrefix(line, "Generated-by:") && !strings.HasPrefix(line, "Assisted-by:") {
			continue
		}

		for _, match := range startedAt.FindAllStringSubmatch(line, -1) {
			candidate, parseErr := time.Parse("2006-01-02T15:04:05Z", match[1])
			if parseErr == nil && candidate.Before(start) {
				start, source = candidate, "started_at"
			}
		}
	}

	return start, source
}
