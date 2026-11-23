// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package recorder_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const (
	workflowJobEvent = "workflow_job"
	ciFailuresName   = "ci_job_failures_total"
	completedAction  = "completed"
	failureResult    = "failure"
	workflowKey      = "workflow"
	jobKey           = "job"
	completedAtKey   = "completed_at"
	headSHAKey       = "head_sha"
)

func TestRecordFailedWorkflowJob(t *testing.T) {
	t.Parallel()

	const body = `{"action":"completed","repository":{"full_name":"tbhb-dev/github-metrics"},"workflow_job":{
		"workflow_name":"CI","name":"build","head_branch":"main","head_sha":"123abc","run_id":493,
		"started_at":"2026-11-15T12:00:00Z","completed_at":"2026-11-15T12:01:00Z","conclusion":"failure",
		"html_url":"omit job URL","steps":[{"name":"checkout","conclusion":"success"},
		{"name":"compile","conclusion":"failure"},{"name":"test","conclusion":"failure"},
		{"name":"three","conclusion":"failure"},{"name":"four","conclusion":"failure"},
		{"name":"five","conclusion":"failure"},{"name":"six","conclusion":"failure"},
		{"name":"seven","conclusion":"failure"},{"name":"eight","conclusion":"failure"},
		{"name":"nine","conclusion":"failure"}]}}`

	var logs bytes.Buffer

	consumer, reader := newRecorderWithLogger(t, newLedger(nil), slog.New(slog.NewJSONHandler(&logs, nil)))

	err := consumer.Record(t.Context(), workflowJobEvent, []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	assertFailedJobLog(t, &logs, map[string]any{
		repositoryKey: repositoryName, workflowKey: "CI", jobKey: buildName,
		"failed_steps": "compile, test, three, four, five, six, seven, eight",
		headBranchKey:  "main", headSHAKey: "123abc", "run_id": float64(493),
		startedAtKey: "2026-11-15T12:00:00Z", completedAtKey: "2026-11-15T12:01:00Z",
	})
	checkMetrics(t, reader, map[string]map[string]string{
		eventsName:     {repositoryKey: repositoryName, eventKey: workflowJobEvent, actionKey: completedAction},
		ciFailuresName: {repositoryKey: repositoryName, workflowKey: "CI", jobKey: buildName},
	})
}

func TestNonFailureWorkflowJobs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ event, action, conclusion string }{
		{workflowJobEvent, completedAction, successResult},
		{workflowJobEvent, completedAction, "cancelled"},
		{workflowJobEvent, "in_progress", failureResult},
		{"push", completedAction, failureResult},
	} {
		t.Run(test.event+"/"+test.action+"/"+test.conclusion, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			consumer, reader := newRecorderWithLogger(t, newLedger(nil), slog.New(slog.NewJSONHandler(&logs, nil)))
			body := `{"action":"` + test.action + `","workflow_job":{"conclusion":"` + test.conclusion + `"}}`

			err := consumer.Record(t.Context(), test.event, []byte(body))
			if err != nil {
				t.Fatal(err)
			}

			if logs.Len() != 0 {
				t.Fatalf("unexpected job log: %s", &logs)
			}

			if got := collectCounters(t, reader)[ciFailuresName]; got != nil {
				t.Fatalf("unexpected failure series: %v", got)
			}

			checkMetrics(t, reader, map[string]map[string]string{
				eventsName: {repositoryKey: unknownRepository, eventKey: test.event, actionKey: test.action},
			})
		})
	}
}

func TestFailedWorkflowJobCleansText(t *testing.T) {
	t.Parallel()

	longText := "\r\n" + strings.Repeat("é", 129)
	logText, labelText := strings.Repeat("é", 128), strings.Repeat("é", 64)

	body, err := json.Marshal(map[string]any{
		actionKey: completedAction,
		workflowJobEvent: map[string]any{
			"workflow_name": longText, nameKey: longText, conclusionKey: failureResult,
			headBranchKey: longText, headSHAKey: longText, startedAtKey: longText, completedAtKey: longText,
			"steps": []map[string]string{{nameKey: longText, conclusionKey: failureResult}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer

	consumer, reader := newRecorderWithLogger(t, newLedger(nil), slog.New(slog.NewJSONHandler(&logs, nil)))

	err = consumer.Record(t.Context(), workflowJobEvent, body)
	if err != nil {
		t.Fatal(err)
	}

	assertFailedJobLog(t, &logs, map[string]any{
		repositoryKey: unknownRepository, workflowKey: logText, jobKey: logText, "failed_steps": logText,
		headBranchKey: logText, headSHAKey: logText, "run_id": float64(0),
		startedAtKey: logText, completedAtKey: logText,
	})
	checkMetrics(t, reader, map[string]map[string]string{
		eventsName:     {repositoryKey: unknownRepository, eventKey: workflowJobEvent, actionKey: completedAction},
		ciFailuresName: {repositoryKey: unknownRepository, workflowKey: labelText, jobKey: labelText},
	})
}

func assertFailedJobLog(t *testing.T, logs *bytes.Buffer, want map[string]any) {
	t.Helper()

	var got map[string]any

	err := json.Unmarshal(logs.Bytes(), &got)
	if err != nil {
		t.Fatalf("want one JSON log, got %q: %v", logs, err)
	}

	delete(got, "time")

	want["level"], want["msg"] = "INFO", "ci job failed"

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %v, want %v", got, want)
	}
}
