// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package main

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

func TestNewRecorderWithoutEndpoint(t *testing.T) {
	t.Parallel()

	consumer, err := newRecorder("", nil)
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

	consumer, err := newRecorder("configured", provider.Meter("test"))
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

	if len(data.ScopeMetrics) != 1 || len(data.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("scope metrics = %v, want one counter", data.ScopeMetrics)
	}

	measurement := data.ScopeMetrics[0].Metrics[0]
	sum, ok := measurement.Data.(metricdata.Sum[int64])

	if measurement.Name != "github.events" || !ok || len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("metric = %v, want one github.events observation", measurement)
	}
}
