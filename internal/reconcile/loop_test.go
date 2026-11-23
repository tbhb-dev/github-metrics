// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package reconcile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/reconcile"
)

func TestLoopSchedule(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		calls := 0
		release := make(chan struct{})
		pass := func(ctx context.Context) (reconcile.RunResult, error) {
			calls++

			return waitForPass(ctx, t, calls, release), nil
		}

		loop, err := reconcile.NewLoop(pass, noop.NewMeterProvider().Meter("test"), slog.New(slog.DiscardHandler), nil)
		if err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() { done <- loop.Start(ctx) }()

		synctest.Wait()

		if calls != 1 {
			t.Errorf("startup calls = %d, want 1", calls)
		}

		time.Sleep(17 * time.Minute)
		synctest.Wait()

		if calls != 1 {
			t.Errorf("calls after the busy tick = %d, want 1", calls)
		}

		time.Sleep(13 * time.Minute)
		synctest.Wait()

		if calls != 2 {
			t.Errorf("calls at the second tick = %d, want 2", calls)
		}

		cancel()
		synctest.Wait()

		select {
		case earlyErr := <-done:
			t.Fatalf("loop returned %v before its running pass finished", earlyErr)
		default:
		}

		close(release)

		err = <-done
		if err != nil {
			t.Fatal(err)
		}
	})
}

func waitForPass(ctx context.Context, t *testing.T, call int, release <-chan struct{}) reconcile.RunResult {
	t.Helper()

	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) != 14*time.Minute {
		t.Errorf("pass deadline = %v, want 14 minutes from now", deadline)
	}

	<-ctx.Done()

	if call == 1 {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("first pass context error = %v, want deadline exceeded", ctx.Err())
		}

		// Leave the startup pass busy past the first tick.
		time.Sleep(2 * time.Minute)
	} else {
		<-release
	}

	var result reconcile.RunResult

	result.Result = reconcile.RunPartial

	return result
}

func TestLoopReportsPasses(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

		var logs bytes.Buffer

		statuses := []reconcile.RunStatus{
			reconcile.RunOK,
			reconcile.RunPartial,
			reconcile.RunAuthFailed,
			reconcile.RunPartial,
		}
		firstSuccess := time.Now().Add(2 * time.Second)
		calls := 0
		pass := func(context.Context) (reconcile.RunResult, error) {
			time.Sleep(2 * time.Second)

			result := reconcile.RunResult{
				Result: statuses[calls], Repositories: 3, Pages: 4, NotModified: 1, Truncated: 1,
				Recorded: 2, AlreadyCounted: 5, TooOld: 0, PreFloor: 0, Skipped: 0,
			}
			calls++

			if calls == len(statuses) {
				result.Result = ""

				return result, fmt.Errorf("store error with request details: %w", io.ErrClosedPipe)
			}

			return result, nil
		}

		loop, err := reconcile.NewLoop(pass, provider.Meter("test"), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
		if err != nil {
			t.Fatal(err)
		}

		err = loop.Start(t.Context())
		if !errors.Is(err, io.ErrClosedPipe) || calls != len(statuses) {
			t.Fatalf("loop returned %v after %d passes", err, calls)
		}

		checkLoopMetrics(t, reader, firstSuccess)
		checkLoopLogs(t, &logs, statuses)
	})
}

func checkLoopMetrics(t *testing.T, reader *sdkmetric.ManualReader, lastSuccess time.Time) {
	t.Helper()

	const (
		prefix = "github_metrics.reconcile."
		kind   = "kind=incremental"
		runs   = prefix + "runs//counter/" + kind
	)

	var data metricdata.ResourceMetrics

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	if len(data.ScopeMetrics) != 1 {
		t.Fatalf("metric scopes = %d, want 1", len(data.ScopeMetrics))
	}

	got := map[string]float64{}
	encoder := attribute.DefaultEncoder()

	for _, measurement := range data.ScopeMetrics[0].Metrics {
		key := measurement.Name + "/" + measurement.Unit + "/"

		var intPoints []metricdata.DataPoint[int64]

		switch points := measurement.Data.(type) {
		case metricdata.Sum[int64]:
			intPoints = points.DataPoints
			key += "counter/"
		case metricdata.Gauge[int64]:
			intPoints = points.DataPoints
			key += "gauge/"
		case metricdata.Histogram[float64]:
			for _, point := range points.DataPoints {
				series := key + point.Attributes.Encoded(encoder)
				got[series] = point.Sum
				got[series+"/count"] = float64(point.Count)
			}
		default:
			t.Fatalf("unexpected metric %v", measurement)
		}

		for _, point := range intPoints {
			got[key+point.Attributes.Encoded(encoder)] = float64(point.Value)
		}
	}

	want := map[string]float64{
		runs + ",result=ok":                                       1,
		runs + ",result=partial":                                  2,
		runs + ",result=auth_failed":                              1,
		prefix + "duration/s/" + kind:                             8,
		prefix + "duration/s/" + kind + "/count":                  4,
		prefix + "repairs//counter/" + kind + ",milestone=merged": 8,
		prefix + "last_success/s/gauge/" + kind:                   float64(lastSuccess.Unix()),
	}
	if !maps.Equal(got, want) {
		t.Errorf("metrics = %v, want %v", got, want)
	}
}

func checkLoopLogs(t *testing.T, logs io.Reader, statuses []reconcile.RunStatus) {
	t.Helper()

	decoder := json.NewDecoder(logs)

	for _, status := range statuses {
		var got map[string]any

		err := decoder.Decode(&got)
		if err != nil {
			t.Fatal(err)
		}

		delete(got, slog.TimeKey)

		want := map[string]any{
			slog.LevelKey: "INFO", slog.MessageKey: "reconcile pass finished",
			"kind": "incremental", "result": string(status),
			"repos": float64(3), "pages": float64(4), "not_modified": float64(1), "recorded": float64(2),
			"already_counted": float64(5), "truncated": float64(1), "duration": float64(2 * time.Second),
		}
		if !maps.Equal(got, want) {
			t.Errorf("log = %v, want %v", got, want)
		}
	}

	if decoder.More() {
		t.Error("extra pass log")
	}
}
