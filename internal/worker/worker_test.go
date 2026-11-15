// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package worker_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/tbhb-dev/github-metrics/internal/store"
	"github.com/tbhb-dev/github-metrics/internal/worker"
)

const maxQueueReads = 10

const removeFirst = "remove 1"

const batchSize = 100

const pullRequestEvent = "pull_request"

const openedBody = `{"repository":{"full_name":"tbhb-dev/github-metrics"},"action":"opened"}`

type queueStub struct {
	t       *testing.T
	pending func(int) ([]store.Entry, error)
	remove  func(uint64) error
	reads   int
}

func (q *queueStub) Pending(limit int) ([]store.Entry, error) {
	q.t.Helper()
	q.reads++

	// Bound broken drain loops during mutation checks.
	if q.reads > maxQueueReads {
		q.t.Fatal("worker kept reading the queue")
	}

	return q.pending(limit)
}

func (q *queueStub) Remove(seq uint64) error { return q.remove(seq) }

type recorderFunc func(string, string)

func (f recorderFunc) RecordPullRequest(repository, action string) { f(repository, action) }

func TestParse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, body, repository, action string
		bad                            bool
	}{
		{"opened", openedBody, "tbhb-dev/github-metrics", "opened", false},
		{
			"closed with unused fields",
			`{"action":"closed","repository":{"full_name":"tbhb-dev/wiki","id":1},"pull_request":{"number":2}}`,
			"tbhb-dev/wiki", "closed", false,
		},
		{"bad JSON", `{"action":`, "", "", true},
		{"bad action type", `{"action":1}`, "", "", true},
		{"bad repository type", `{"repository":{"full_name":1}}`, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repository, action, err := worker.Parse([]byte(test.body))
			if (err != nil) != test.bad || repository != test.repository || action != test.action {
				t.Fatalf("parse = %q, %q, %v", repository, action, err)
			}
		})
	}
}

func TestPass(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, event, body, log string
		want                   []string
	}{
		{
			"record before remove", pullRequestEvent, openedBody, "",
			[]string{"record tbhb-dev/github-metrics opened", removeFirst},
		},
		{"bad body removed", pullRequestEvent, `{"action":`, "bad pull request body", []string{removeFirst}},
		{"other event removed", "ping", openedBody, "discarded event", []string{removeFirst}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls []string

			var logs bytes.Buffer

			entries := []store.Entry{{Delivery: "d1", Event: test.event, Body: []byte(test.body), Seq: 1}}
			queue := queueStub{
				t:       t,
				reads:   0,
				pending: func(int) ([]store.Entry, error) { return entries, nil },
				remove: func(seq uint64) error {
					calls = append(calls, fmt.Sprintf("remove %d", seq))
					entries = nil

					return nil
				},
			}
			recorder := recorderFunc(func(repository, action string) {
				calls = append(calls, "record "+repository+" "+action)
			})

			err := worker.New(&queue, recorder, slog.New(slog.NewTextHandler(&logs, nil))).Pass(t.Context())
			if err != nil || !reflect.DeepEqual(calls, test.want) || !strings.Contains(logs.String(), test.log) {
				t.Fatalf("Pass: %v, calls = %v, logs = %q", err, calls, logs.String())
			}
		})
	}
}

func TestPassDrainsBatches(t *testing.T) {
	t.Parallel()

	const total = batchSize + 1

	var removed, recorded uint64

	var reads int

	queue := queueStub{
		t:     t,
		reads: 0,
		pending: func(limit int) ([]store.Entry, error) {
			reads++

			if limit != batchSize {
				t.Fatalf("limit = %d, want %d", limit, batchSize)
			}

			var entries []store.Entry

			for seq := removed + 1; seq <= total && len(entries) < limit; seq++ {
				entries = append(entries, store.Entry{
					Delivery: "d1", Event: pullRequestEvent, Body: []byte(openedBody), Seq: seq,
				})
			}

			return entries, nil
		},
		remove: func(seq uint64) error {
			removed++

			if seq != removed || recorded != removed {
				t.Fatalf("Remove(%d): removed = %d, recorded = %d", seq, removed, recorded)
			}

			return nil
		},
	}
	consumer := worker.New(&queue, recorderFunc(func(_, _ string) { recorded++ }), slog.New(slog.DiscardHandler))

	err := consumer.Pass(t.Context())
	if err != nil || recorded != total || removed != total || reads != 3 {
		t.Fatalf("Pass: %v, recorded = %d, removed = %d, reads = %d", err, recorded, removed, reads)
	}
}
