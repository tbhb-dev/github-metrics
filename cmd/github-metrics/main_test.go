// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

var errFloorRead = errors.New("floor read failed")

type stubLedger struct {
	floor    time.Time
	floorErr error
}

func (stubLedger) Created() time.Time                  { return time.Time{} }
func (s stubLedger) RepairFloor() (time.Time, error)   { return s.floor, s.floorErr }
func (stubLedger) ClaimMilestone(string) (bool, error) { return true, nil }

func TestLogRepairFloor(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logs, nil))
	floor := time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC)

	err := logRepairFloor(log, stubLedger{floor: floor, floorErr: nil})
	if err != nil {
		t.Fatal(err)
	}

	var entry map[string]string

	err = json.Unmarshal(logs.Bytes(), &entry)
	if err != nil {
		t.Fatal(err)
	}

	if entry["level"] != "INFO" || entry["repair_floor"] != floor.Format(time.RFC3339) || len(entry) != 4 {
		t.Fatalf("log entry = %v, want one info line with only the floor as an attribute", entry)
	}
}

func TestLogRepairFloorReadError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logs, nil))

	err := logRepairFloor(log, stubLedger{floor: time.Time{}, floorErr: errFloorRead})
	if !errors.Is(err, errFloorRead) || logs.Len() != 0 {
		t.Fatalf("logRepairFloor error = %v, log = %q; want %v and no log", err, logs.String(), errFloorRead)
	}
}

func TestNewRecorderWithoutLedger(t *testing.T) {
	t.Parallel()

	_, err := newRecorder("configured", nil, nil)
	if !errors.Is(err, recorder.ErrNoLedger) {
		t.Fatalf("newRecorder error = %v, want %v", err, recorder.ErrNoLedger)
	}
}

func TestNewRecorderWithoutEndpoint(t *testing.T) {
	t.Parallel()

	consumer, err := newRecorder("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := consumer.(noopRecorder); !ok {
		t.Fatalf("recorder has type %T, want noopRecorder", consumer)
	}

	err = consumer.Record(t.Context(), "ping", []byte(`{}`))
	if !errors.Is(err, errNoRecorder) {
		t.Fatalf("Record error = %v, want %v", err, errNoRecorder)
	}
}

func TestNewRecorderWithEndpoint(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	t.Cleanup(func() {
		shutdownErr := provider.Shutdown(t.Context())
		if shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})

	var ledger stubLedger

	consumer, err := newRecorder("configured", provider.Meter("test"), ledger)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := consumer.(*recorder.Recorder); !ok {
		t.Fatalf("recorder has type %T, want *recorder.Recorder", consumer)
	}

	err = consumer.Record(t.Context(), "ping", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	var data metricdata.ResourceMetrics

	err = reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	if len(data.ScopeMetrics) != 1 || len(data.ScopeMetrics[0].Metrics) == 0 {
		t.Fatalf("scope metrics = %v, want counter observations", data.ScopeMetrics)
	}

	measurement := data.ScopeMetrics[0].Metrics[0]
	sum, ok := measurement.Data.(metricdata.Sum[int64])

	if measurement.Name != "github.events" || !ok || len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("metric = %v, want one github.events observation", measurement)
	}
}
