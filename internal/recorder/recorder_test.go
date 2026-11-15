// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package recorder_test

import (
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tbhb-dev/github-metrics/internal/recorder"
)

const (
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
				eventsName:         {repositoryKey: "tbhb-dev/github-metrics", eventKey: pullRequestEvent, actionKey: "closed"},
				"github.pr.merges": {repositoryKey: "tbhb-dev/github-metrics"},
			},
			"merge", pullRequestEvent,
			`{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"closed",` +
				`"pull_request":{"merged":true,"title":"omit title","body":"omit body",` +
				`"head":{"ref":"omit branch"},"user":{"login":"omit login"}},"comment":{"body":"omit text"}}`,
		},
		{
			map[string]map[string]string{
				eventsName: {repositoryKey: unknownRepository, eventKey: pullRequestEvent, actionKey: "closed"},
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
			map[string]map[string]string{"github.events.dropped": {"reason": "empty_event"}},
			"legacy entry", "", `{}`,
		},
		{
			map[string]map[string]string{"github.events.dropped": {"reason": "bad_body"}},
			"bad JSON", pullRequestEvent, `{"action":`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

			t.Cleanup(func() {
				shutdownErr := provider.Shutdown(t.Context())
				if shutdownErr != nil {
					t.Error(shutdownErr)
				}
			})

			consumer, err := recorder.New(provider.Meter("test"))
			if err != nil {
				t.Fatal(err)
			}

			err = consumer.Record(t.Context(), test.event, []byte(test.body))
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
