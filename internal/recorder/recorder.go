// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package recorder counts queued GitHub deliveries as metrics.
package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Recorder counts events without recording webhook text in attributes.
type Recorder struct {
	events, merges, conclusions, dropped metric.Int64Counter
	cycleTime                            metric.Float64Histogram
}

type pullRequest struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"` //nolint:tagliatelle // GitHub uses this field name.
	MergedAt  string `json:"merged_at"`  //nolint:tagliatelle // GitHub uses this field name.
	ClosedAt  string `json:"closed_at"`  //nolint:tagliatelle // GitHub uses this field name.
	Merged    bool   `json:"merged"`
}

// New creates the instruments used to record deliveries.
//
//nolint:mnd // The histogram has fixed cycle time buckets in seconds.
func New(meter metric.Meter) (*Recorder, error) {
	var recorder Recorder

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

	var err error

	recorder.cycleTime, err = meter.Float64Histogram("github.pr.cycle_time", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800, 1209600,
		))
	if err != nil {
		return nil, fmt.Errorf("create github.pr.cycle_time histogram: %w", err)
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
			FullName string `json:"full_name"` //nolint:tagliatelle // GitHub uses this field name.
		} `json:"repository"`
		Action   string `json:"action"`
		CheckRun struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"check_run"` //nolint:tagliatelle // GitHub uses this field name.
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

	repo := attribute.String("repository", repository)
	r.events.Add(ctx, 1, metric.WithAttributes(repo,
		attribute.String("event", event), attribute.String("action", delivery.Action)))

	if event == "pull_request" && delivery.Action == "closed" {
		if delivery.PullRequest.Merged {
			r.merges.Add(ctx, 1, metric.WithAttributes(repo))
		}

		r.recordCycleTime(ctx, repo, delivery.PullRequest)
	}

	if event == "check_run" && delivery.Action == "completed" {
		r.conclusions.Add(ctx, 1, metric.WithAttributes(repo,
			attribute.String("check_name", delivery.CheckRun.Name),
			attribute.String("conclusion", delivery.CheckRun.Conclusion)))
	}

	return nil
}

func (r *Recorder) recordCycleTime(ctx context.Context, repo attribute.KeyValue, request pullRequest) {
	endText, state := request.ClosedAt, "closed"
	if request.Merged {
		endText, state = request.MergedAt, "merged"
	}

	end, err := time.Parse(time.RFC3339, endText)
	if err != nil {
		return
	}

	start, source := cycleStart(request)
	if start.IsZero() || end.Before(start) {
		return
	}

	r.cycleTime.Record(ctx, end.Sub(start).Seconds(), metric.WithAttributes(repo,
		attribute.String("state", state), attribute.String("start_source", source)))
}

func cycleStart(request pullRequest) (time.Time, string) {
	start, err := time.Parse(time.RFC3339, request.CreatedAt)
	if err != nil {
		return time.Time{}, ""
	}

	// pr_created is interim: the owner's rule starts at the earliest commit time.
	// That needs the REST commits call in a later change, so interim values read low.
	source := "pr_created"
	startedAt := regexp.MustCompile(`\bstarted_at[:=]\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`)

	for line := range strings.Lines(request.Body) {
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
