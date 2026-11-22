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
		{"multi-day bucket", pullRequestEvent, closedState, created, "2026-11-18T12:00:00Z", "", createdSource, true, 259200},
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
				checkNoDrops(t, measurement)
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

func checkNoDrops(t *testing.T, measurement metricdata.Metrics) {
	t.Helper()

	sum, ok := measurement.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("unexpected dropped data: %v", measurement.Data)
	}

	for _, point := range sum.DataPoints {
		if point.Value != 0 {
			t.Fatal("skipped cycle time counted as a drop")
		}
	}
}

func checkCycleHistogram(t *testing.T, measurement metricdata.Metrics, state, source string, seconds float64) {
	t.Helper()

	histogram, ok := measurement.Data.(metricdata.Histogram[float64])
	if !ok || measurement.Unit != "s" || len(histogram.DataPoints) != 1 {
		t.Fatalf("expected one histogram in seconds, got %v", measurement)
	}

	point := histogram.DataPoints[0]
	bounds := []float64{
		60, 300, 600, 900, 1800, 3600, 7200, 14400, 28800,
		86400, 172800, 259200, 345600, 432000, 518400, 604800, 1209600, 2592000,
	}
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
			recordCounter(t, measurement, got)
		}
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metrics = %v, want %v", got, want)
	}
}

func recordCounter(t *testing.T, measurement metricdata.Metrics, got map[string]map[string]string) {
	t.Helper()

	sum, ok := measurement.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("%s: expected monotonic counter, got %v", measurement.Name, measurement.Data)
	}

	for _, point := range sum.DataPoints {
		if point.Value == 0 {
			continue
		}

		if point.Value != 1 || got[measurement.Name] != nil {
			t.Fatalf("%s: expected at most one counter observation of 1, got %v", measurement.Name, sum.DataPoints)
		}

		attrs := make(map[string]string)
		for _, attr := range point.Attributes.ToSlice() {
			attrs[string(attr.Key)] = attr.Value.AsString()
		}

		got[measurement.Name] = attrs
	}
}

func TestSeriesStartAtZero(t *testing.T) {
	t.Parallel()

	consumer, reader := newRecorder(t)

	if got := collectCounters(t, reader); !reflect.DeepEqual(got, map[string]map[string]int64{
		droppedName: {"reason=bad_body": 0, "reason=empty_event": 0},
	}) {
		t.Fatalf("startup counters = %v", got)
	}

	err := consumer.Record(t.Context(), checkRunEvent, []byte(
		`{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"created","check_run":{"name":"build"}}`,
	))
	if err != nil {
		t.Fatal(err)
	}

	before := collectCounters(t, reader)

	// A missing key also reads 0, so assert the merge series exists.
	merges, seeded := before["github.pr.merges"]["repository=tbhb-dev/github-metrics"]
	if !seeded || merges != 0 || len(before["github.check.conclusions"]) != 9 {
		t.Fatalf("seeded repository counters = %v", before)
	}

	for key, val := range before["github.check.conclusions"] {
		if val != 0 {
			t.Fatalf("conclusion %s = %d, want 0", key, val)
		}
	}

	for _, step := range []struct{ event, body string }{
		{
			pullRequestEvent,
			`{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"closed","pull_request":{"merged":true}}`,
		},
		{
			checkRunEvent,
			`{"repository":{"full_name":"tbhb-dev/github-metrics"},` +
				`"action":"completed","check_run":{"name":"build","conclusion":"success"}}`,
		},
		{"", `{}`},
		{pullRequestEvent, `{`},
	} {
		err = consumer.Record(t.Context(), step.event, []byte(step.body))
		if err != nil {
			t.Fatal(err)
		}
	}

	after := collectCounters(t, reader)
	if after["github.pr.merges"]["repository=tbhb-dev/github-metrics"] != 1 ||
		after["github.check.conclusions"]["check_name=build,conclusion=success,repository=tbhb-dev/github-metrics"] != 1 ||
		after["github.check.conclusions"]["check_name=build,conclusion=failure,repository=tbhb-dev/github-metrics"] != 0 ||
		after[droppedName]["reason=empty_event"] != 1 ||
		after[droppedName]["reason=bad_body"] != 1 {
		t.Fatalf("counters after first real Add = %v", after)
	}
}

func collectCounters(t *testing.T, reader *sdkmetric.ManualReader) map[string]map[string]int64 {
	t.Helper()

	var data metricdata.ResourceMetrics

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]map[string]int64)

	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			sum, ok := measurement.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}

			series := make(map[string]int64, len(sum.DataPoints))
			for _, point := range sum.DataPoints {
				var parts []string
				for _, attr := range point.Attributes.ToSlice() {
					parts = append(parts, string(attr.Key)+"="+attr.Value.AsString())
				}

				series[strings.Join(parts, ",")] = point.Value
			}

			got[measurement.Name] = series
		}
	}

	return got
}

type mainRedStep struct {
	action, checkName, conclusion, headBranch, defaultBranch string
	wantValue                                                int64
	wantRecorded                                             bool
}

func TestRecordMainRed(t *testing.T) {
	t.Parallel()

	const (
		mainBranch      = "main"
		conclusionCheck = "conclusion"
		completedAction = "completed"
		failureResult   = "failure"
	)

	for _, test := range []struct {
		name  string
		steps []mainRedStep
	}{
		{
			"red on main",
			[]mainRedStep{{completedAction, conclusionCheck, failureResult, mainBranch, mainBranch, 1, true}},
		},
		{
			"timed out on main",
			[]mainRedStep{{completedAction, conclusionCheck, "timed_out", mainBranch, mainBranch, 1, true}},
		},
		{
			"later green on main",
			[]mainRedStep{
				{completedAction, conclusionCheck, failureResult, mainBranch, mainBranch, 1, true},
				{completedAction, conclusionCheck, "success", mainBranch, mainBranch, 0, true},
			},
		},
		{
			"red on pull request branch",
			[]mainRedStep{{completedAction, conclusionCheck, failureResult, "feat/pr", mainBranch, 0, false}},
		},
		{
			"non-conclusion check",
			[]mainRedStep{{completedAction, "build", failureResult, mainBranch, mainBranch, 0, false}},
		},
		{
			"empty conclusion",
			[]mainRedStep{{completedAction, conclusionCheck, "", mainBranch, mainBranch, 0, false}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			consumer, reader := newRecorder(t)

			for _, step := range test.steps {
				body, err := json.Marshal(map[string]any{
					"repository": map[string]string{
						"full_name":      repositoryName,
						"default_branch": step.defaultBranch,
					},
					"action": step.action,
					"check_run": map[string]any{
						"name":        step.checkName,
						"conclusion":  step.conclusion,
						"check_suite": map[string]string{"head_branch": step.headBranch},
					},
				})
				if err != nil {
					t.Fatal(err)
				}

				err = consumer.Record(t.Context(), checkRunEvent, body)
				if err != nil {
					t.Fatal(err)
				}

				checkMainRedGauge(t, reader, step.wantRecorded, step.wantValue)
			}
		})
	}
}

func checkMainRedGauge(t *testing.T, reader *sdkmetric.ManualReader, wantRecorded bool, wantValue int64) {
	t.Helper()

	var data metricdata.ResourceMetrics

	err := reader.Collect(t.Context(), &data)
	if err != nil {
		t.Fatal(err)
	}

	found := false

	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name != "github.main.red" {
				continue
			}

			found = true

			checkMainRedPoint(t, measurement, wantValue)
		}
	}

	if found != wantRecorded {
		t.Fatalf("github.main.red present = %v, want %v", found, wantRecorded)
	}
}

func checkMainRedPoint(t *testing.T, measurement metricdata.Metrics, wantValue int64) {
	t.Helper()

	gauge, ok := measurement.Data.(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) != 1 {
		t.Fatalf("expected one int64 gauge point, got %v", measurement)
	}

	point := gauge.DataPoints[0]

	attrs := make(map[string]string)
	for _, attr := range point.Attributes.ToSlice() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}

	wantAttrs := map[string]string{repositoryKey: repositoryName}
	if !reflect.DeepEqual(attrs, wantAttrs) || point.Value != wantValue {
		t.Fatalf("gauge = (%v, %d), want (%v, %d)", attrs, point.Value, wantAttrs, wantValue)
	}
}
