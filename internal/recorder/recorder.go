// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package recorder counts queued GitHub deliveries as metrics.
package recorder

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Recorder counts events without recording webhook text in attributes.
type Recorder struct {
	events, merges, conclusions, dropped metric.Int64Counter
}

// New creates the counters used to record deliveries.
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
		PullRequest struct {
			Merged bool `json:"merged"`
		} `json:"pull_request"` //nolint:tagliatelle // GitHub uses this field name.
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

	if event == "pull_request" && delivery.Action == "closed" && delivery.PullRequest.Merged {
		r.merges.Add(ctx, 1, metric.WithAttributes(repo))
	}

	if event == "check_run" && delivery.Action == "completed" {
		r.conclusions.Add(ctx, 1, metric.WithAttributes(repo,
			attribute.String("check_name", delivery.CheckRun.Name),
			attribute.String("conclusion", delivery.CheckRun.Conclusion)))
	}

	return nil
}
