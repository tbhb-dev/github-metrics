// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package telemetry_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/protobuf/proto"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/tbhb-dev/github-metrics/internal/telemetry"
)

func TestSetupWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, meter, err := telemetry.Setup(t.Context(), "test-service", "test-version")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := meter.(noop.Meter); !ok {
		t.Fatalf("meter has type %T, want noop.Meter", meter)
	}

	shutdownErr := shutdown(t.Context())
	if shutdownErr != nil {
		t.Fatal(shutdownErr)
	}
}

func TestSetupExportsOnShutdown(t *testing.T) {
	requests := make(chan []byte, 1)
	server := exportServer(t, requests)

	defer server.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "delta")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "3600000")

	shutdown, meter, err := telemetry.Setup(t.Context(), "test-service", "test-version")
	if err != nil {
		t.Fatal(err)
	}

	counter, err := meter.Int64Counter("test.counter")
	if err != nil {
		t.Fatal(err)
	}

	counter.Add(t.Context(), 1)

	shutdownErr := shutdown(t.Context())
	if shutdownErr != nil {
		t.Fatal(shutdownErr)
	}

	if got := len(requests); got != 1 {
		t.Fatalf("export requests = %d, want 1", got)
	}

	checkExport(t, <-requests)
}

func exportServer(t *testing.T, requests chan<- []byte) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/metrics" {
			t.Errorf("request = %s %s, want POST /v1/metrics", request.Method, request.URL.Path)
		}

		if got := request.Header.Get("Content-Type"); got != "application/x-protobuf" {
			t.Errorf("Content-Type = %q, want application/x-protobuf", got)
		}

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}

		requests <- body

		writer.Header().Set("Content-Type", "application/x-protobuf")
	}))
}

func checkExport(t *testing.T, body []byte) {
	t.Helper()

	var request collectorpb.ExportMetricsServiceRequest

	err := proto.Unmarshal(body, &request)
	if err != nil {
		t.Fatal(err)
	}

	metrics := request.GetResourceMetrics()
	if len(metrics) != 1 {
		t.Fatalf("resource metrics = %d, want 1", len(metrics))
	}

	attrs := make(map[string]string)
	for _, attr := range metrics[0].GetResource().GetAttributes() {
		attrs[attr.GetKey()] = attr.GetValue().GetStringValue()
	}

	if attrs["service.name"] != "test-service" || attrs["service.version"] != "test-version" {
		t.Errorf("resource attributes = %v, want service name and version", attrs)
	}

	scopes := metrics[0].GetScopeMetrics()
	if len(scopes) != 1 || len(scopes[0].GetMetrics()) != 1 {
		t.Fatalf("scope metrics = %v, want one counter", scopes)
	}

	exported := scopes[0].GetMetrics()[0]
	sum := exported.GetSum()

	if exported.GetName() != "test.counter" ||
		sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Errorf("exported metric = %v, want cumulative test.counter", exported)
	}

	points := sum.GetDataPoints()
	if len(points) != 1 || points[0].GetAsInt() != 1 {
		t.Errorf("data points = %v, want one observation of 1", points)
	}
}
