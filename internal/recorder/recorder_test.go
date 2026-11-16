// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package recorder_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

type cycleTimeTest struct {
	name, event, action, createdAt, end, body, source string
	merged                                            bool
	seconds                                           float64
}

func TestRecordCycleTime(t *testing.T) {
	t.Parallel()

	const (
		created = "2026-11-15T12:00:00Z"
		ended   = "2026-11-15T13:00:00Z"
		earlier = "2026-11-15T11:00:00Z"
		trailer = "Generated-by: discard this text; started_at="
	)

	for _, test := range []cycleTimeTest{
		{"created only", pullRequestEvent, closedState, created, ended, "", createdSource, true, 3600},
		{"earlier trailer", pullRequestEvent, closedState, created, ended, trailer + earlier, startedAtSource, true, 7200},
		{"later trailer", pullRequestEvent, closedState, created, ended, trailer + ended, createdSource, true, 3600},
		{closedState, pullRequestEvent, closedState, created, ended, "", createdSource, false, 3600},
		{"end before start", pullRequestEvent, closedState, created, earlier, "", "", true, 0},
		{
			"malformed trailer", pullRequestEvent, closedState, created, ended,
			trailer + "2026-99-15T11:00:00Z\n" + trailer + "2026-11-15T11:00:00+00:00", createdSource, true, 3600,
		},
		{
			"two trailers", pullRequestEvent, closedState, created, ended,
			trailer + created + "\nAssisted-by: discard this text; started_at: " + earlier, startedAtSource, true, 7200,
		},
		{"missing merged time", pullRequestEvent, closedState, created, "", "", "", true, 0},
		{
			"no body text retained", pullRequestEvent, closedState, created, ended,
			"discard this text\n" + trailer + earlier, startedAtSource, true, 7200,
		},
		{
			"plain body ignored", pullRequestEvent, closedState, created, ended,
			"started_at=" + earlier, createdSource, true, 3600,
		},
		{"missing created time", pullRequestEvent, closedState, "", ended, trailer + earlier, "", true, 0},
		{"bad end time", pullRequestEvent, closedState, created, "invalid", "", "", true, 0},
		{"missing closed time", pullRequestEvent, closedState, created, "", "", "", false, 0},
		{"open PR", pullRequestEvent, "opened", created, ended, trailer + earlier, "", false, 0},
		{"other event", checkRunEvent, closedState, created, ended, trailer + earlier, "", true, 0},
		{"equal times", pullRequestEvent, closedState, created, created, "", createdSource, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reader, state := recordCycleFixture(t, test)

			checkCycleMetrics(t, reader, state, test.source, test.seconds)
		})
	}
}

func recordCycleFixture(t *testing.T, test cycleTimeTest) (*sdkmetric.ManualReader, string) {
	t.Helper()

	consumer, reader := newRecorder(t)

	closedAt, state := test.end, closedState
	if test.merged {
		// Use a different closed_at to prove merged PRs use merged_at.
		closedAt, state = "2026-11-15T10:00:00Z", "merged"
	}

	body, err := json.Marshal(map[string]any{
		"repository": map[string]string{"full_name": repositoryName},
		"action":     test.action,
		"pull_request": map[string]any{
			"merged": test.merged, "created_at": test.createdAt,
			"merged_at": test.end, "closed_at": closedAt, "body": test.body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = consumer.Record(t.Context(), test.event, body)
	if err != nil {
		t.Fatal(err)
	}

	return reader, state
}

func newRecorder(t *testing.T) (*recorder.Recorder, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	t.Cleanup(func() {
		err := provider.Shutdown(t.Context())
		if err != nil {
			t.Error(err)
		}
	})

	consumer, err := recorder.New(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}

	return consumer, reader
}

func checkCycleMetrics(t *testing.T, reader *sdkmetric.ManualReader, state, source string, seconds float64) {
	t.Helper()

	var data metricdata.ResourceMetrics

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(fmt.Sprint(data), "discard this text") {
		t.Fatal("body text retained in metric data")
	}

	found := false

	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name == droppedName {
				t.Fatal("skipped cycle time counted as a drop")
			}

			if measurement.Name != "github.pr.cycle_time" {
				continue
			}

			found = true

			checkCycleHistogram(t, measurement, state, source, seconds)
		}
	}

	if found != (source != "") {
		t.Fatalf("cycle time present = %v, want %v", found, source != "")
	}
}

func checkCycleHistogram(t *testing.T, measurement metricdata.Metrics, state, source string, seconds float64) {
	t.Helper()

	histogram, ok := measurement.Data.(metricdata.Histogram[float64])
	if !ok || measurement.Unit != "s" || len(histogram.DataPoints) != 1 {
		t.Fatalf("expected one histogram in seconds, got %v", measurement)
	}

	point := histogram.DataPoints[0]
	bounds := []float64{60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800, 1209600}
	counts := make([]uint64, len(bounds)+1)
	bucket := 0

	for bucket < len(bounds) && seconds > bounds[bucket] {
		bucket++
	}

	counts[bucket] = 1

	if point.Count != 1 || point.Sum != seconds || !reflect.DeepEqual(point.Bounds, bounds) ||
		!reflect.DeepEqual(point.BucketCounts, counts) {
		t.Fatalf("unexpected cycle time histogram: %v", point)
	}

	attrs := make(map[string]string)
	for _, attr := range point.Attributes.ToSlice() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}

	want := map[string]string{repositoryKey: repositoryName, "state": state, "start_source": source}
	if !reflect.DeepEqual(attrs, want) {
		t.Fatalf("attributes = %v, want %v", attrs, want)
	}
}

const (
	droppedName       = "github.events.dropped"
	closedState       = "closed"
	startedAtSource   = "started_at"
	createdSource     = "pr_created"
	repositoryName    = "tbhb-dev/github-metrics"
	pullRequestEvent  = "pull_request"
	checkRunEvent     = "check_run"
	actionKey         = "action"
	eventKey          = "event"
	eventsName        = "github.events"
	repositoryKey     = "repository"
	unknownRepository = "unknown"
)

func TestRecord(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		want              map[string]map[string]string
		name, event, body string
	}{
		{
			map[string]map[string]string{
				eventsName:         {repositoryKey: repositoryName, eventKey: pullRequestEvent, actionKey: closedState},
				"github.pr.merges": {repositoryKey: repositoryName},
			},
			"merge", pullRequestEvent,
			`{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"closed",` +
				`"pull_request":{"merged":true,"title":"omit title","body":"omit body",` +
				`"head":{"ref":"omit branch"},"user":{"login":"omit login"}},"comment":{"body":"omit text"}}`,
		},
		{
			map[string]map[string]string{
				eventsName: {repositoryKey: unknownRepository, eventKey: pullRequestEvent, actionKey: closedState},
			},
			"closed without merge", pullRequestEvent, `{"action":"closed","pull_request":{"merged":false}}`,
		},
		{
			map[string]map[string]string{
				eventsName:                 {repositoryKey: unknownRepository, eventKey: checkRunEvent, actionKey: "completed"},
				"github.check.conclusions": {repositoryKey: unknownRepository, "check_name": "build", "conclusion": "success"},
			},
			"completed check", checkRunEvent,
			`{"action":"completed","check_run":{"name":"build","conclusion":"success"}}`,
		},
		{
			map[string]map[string]string{
				eventsName: {repositoryKey: unknownRepository, eventKey: checkRunEvent, actionKey: "created"},
			},
			"running check", checkRunEvent, `{"action":"created","check_run":{"name":"build"}}`,
		},
		{
			map[string]map[string]string{
				eventsName: {repositoryKey: unknownRepository, eventKey: "ping", actionKey: ""},
			},
			"other event with empty action", "ping", `{}`,
		},
		{
			map[string]map[string]string{droppedName: {"reason": "empty_event"}},
			"legacy entry", "", `{}`,
		},
		{
			map[string]map[string]string{droppedName: {"reason": "bad_body"}},
			"bad JSON", pullRequestEvent, `{"action":`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			consumer, reader := newRecorder(t)

			err := consumer.Record(t.Context(), test.event, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}

			checkMetrics(t, reader, test.want)
		})
	}
}

func checkMetrics(t *testing.T, reader *sdkmetric.ManualReader, want map[string]map[string]string) {
	t.Helper()

	var data metricdata.ResourceMetrics

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]map[string]string)

	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			sum, ok := measurement.Data.(metricdata.Sum[int64])
			if !ok || !sum.IsMonotonic || len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
				t.Fatalf("%s: expected one counter observation of 1, got %v", measurement.Name, measurement.Data)
			}

			attrs := make(map[string]string)
			for _, attr := range sum.DataPoints[0].Attributes.ToSlice() {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}

			got[measurement.Name] = attrs
		}
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metrics = %v, want %v", got, want)
	}
}
