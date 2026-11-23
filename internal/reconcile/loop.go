// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	passInterval = 15 * time.Minute
	passTimeout  = 14 * time.Minute
	incremental  = "incremental"
	metricPrefix = "github_metrics.reconcile."
	seconds      = "s"
)

// Pass runs one reconciliation. A store error stops the scheduler.
type Pass func(ctx context.Context) (RunResult, error)

// LoopClock supplies pass times and a ticker with a stop function.
type LoopClock interface {
	Clock
	NewTicker(interval time.Duration) (<-chan time.Time, func())
}

// Loop schedules and reports incremental reconciliation passes.
type Loop struct {
	pass          Pass
	clock         LoopClock
	log           *slog.Logger
	runs, repairs metric.Int64Counter
	duration      metric.Float64Histogram
	lastSuccess   metric.Int64Gauge
}

// NewLoop creates a scheduler. A nil clock uses the system clock and ticker.
func NewLoop(pass Pass, meter metric.Meter, log *slog.Logger, clock LoopClock) (*Loop, error) {
	if clock == nil {
		clock = systemLoopClock{}
	}

	var loop Loop

	loop.pass, loop.clock, loop.log = pass, clock, log

	var err error

	loop.runs, err = meter.Int64Counter(metricPrefix + "runs")
	if err != nil {
		return nil, fmt.Errorf("create reconcile runs counter: %w", err)
	}

	loop.repairs, err = meter.Int64Counter(metricPrefix + "repairs")
	if err != nil {
		return nil, fmt.Errorf("create reconcile repairs counter: %w", err)
	}

	loop.duration, err = meter.Float64Histogram(metricPrefix+"duration", metric.WithUnit(seconds))
	if err != nil {
		return nil, fmt.Errorf("create reconcile duration histogram: %w", err)
	}

	loop.lastSuccess, err = meter.Int64Gauge(metricPrefix+"last_success", metric.WithUnit(seconds))
	if err != nil {
		return nil, fmt.Errorf("create reconcile last success gauge: %w", err)
	}

	return &loop, nil
}

// Start runs passes serially until cancellation or a store error. Call it once
// from the goroutine that owns the loop.
func (l *Loop) Start(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}

	ticks, stop := l.clock.NewTicker(passInterval)
	defer stop()

	finished, err := l.runPass(ctx)
	for err == nil && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return nil
		case tick := <-ticks:
			// The ticker may still hold a tick from the previous pass.
			if tick.After(finished) && ctx.Err() == nil {
				finished, err = l.runPass(ctx)
			}
		}
	}

	return err
}

func (l *Loop) runPass(ctx context.Context) (time.Time, error) {
	started := l.clock.Now()
	passCtx, cancel := context.WithTimeout(ctx, passTimeout)
	result, err := l.pass(passCtx)

	cancel()

	finished := l.clock.Now()
	elapsed := finished.Sub(started)

	if err != nil {
		result.Result = RunPartial
	}

	kind := attribute.String("kind", incremental)
	kindOnly := metric.WithAttributes(kind)

	l.runs.Add(ctx, 1, metric.WithAttributes(kind, attribute.String("result", string(result.Result))))
	l.duration.Record(ctx, elapsed.Seconds(), kindOnly)

	// RunResult currently counts recorded merges. First ends arrive with the issue events read.
	l.repairs.Add(ctx, int64(result.Recorded), metric.WithAttributes(kind, attribute.String("milestone", "merged")))

	if result.Result == RunOK {
		l.lastSuccess.Record(ctx, finished.Unix(), kindOnly)
	}

	l.log.InfoContext(
		ctx, "reconcile pass finished",
		slog.String("kind", incremental), slog.String("result", string(result.Result)),
		slog.Int("repos", result.Repositories), slog.Int("pages", result.Pages),
		slog.Int("not_modified", result.NotModified), slog.Int("recorded", result.Recorded),
		slog.Int("already_counted", result.AlreadyCounted), slog.Int("truncated", result.Truncated),
		slog.Duration("duration", elapsed),
	)

	return finished, err
}

type systemLoopClock struct{}

func (systemLoopClock) Now() time.Time { return time.Now() }

func (systemLoopClock) NewTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)

	return ticker.C, ticker.Stop
}
