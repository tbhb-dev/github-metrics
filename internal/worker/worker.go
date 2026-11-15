// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package worker consumes signed webhook bodies from the store queue.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/store"
)

const batchSize = 100

var errMissingFields = errors.New("pull request repository or action is empty")

// Recorder records a pull request event, without a telemetry dependency.
type Recorder interface {
	RecordPullRequest(repository, action string) error
}

// Queue is the part of the store that a worker consumes.
type Queue interface {
	Pending(limit int) ([]store.Entry, error)
	Remove(seq uint64) error
}

// Worker records pull request events and removes consumed queue entries.
type Worker struct {
	queue    Queue
	recorder Recorder
	log      *slog.Logger
}

// New connects a queue to its recorder.
func New(queue Queue, recorder Recorder, log *slog.Logger) *Worker {
	return &Worker{queue: queue, recorder: recorder, log: log}
}

// Run drains the queue at startup and each interval, until cancel or a store error.
func (w *Worker) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		err := w.Pass(ctx)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Pass drains batches until the queue is empty or processing stops. Only a
// successful record removes an entry; otherwise it leaves that entry and the
// rest of its batch queued for the next poll. A store error is returned to the caller.
func (w *Worker) Pass(ctx context.Context) error {
	for ctx.Err() == nil {
		entries, err := w.queue.Pending(batchSize)
		if err != nil {
			return fmt.Errorf("worker pending: %w", err)
		}

		if len(entries) == 0 {
			return nil
		}

		for _, entry := range entries {
			done := ctx.Err() != nil || !w.record(ctx, entry)
			if done {
				return nil
			}

			err = w.queue.Remove(entry.Seq)
			if err != nil {
				return fmt.Errorf("worker remove: %w", err)
			}
		}
	}

	return nil
}

func (w *Worker) record(ctx context.Context, entry store.Entry) bool {
	if entry.Event != "pull_request" {
		w.log.WarnContext(ctx, "unhandled event", "event", oneLine(entry.Event))

		return false
	}

	repository, action, err := parse(entry.Body)
	if err != nil {
		w.log.WarnContext(ctx, "unhandled event", "reason", "bad pull request body")

		return false
	}

	err = w.recorder.RecordPullRequest(repository, action)
	if err != nil {
		w.log.WarnContext(ctx, "recording failed", "event", oneLine(entry.Event))
	}

	return err == nil
}

func parse(body []byte) (string, string, error) {
	var event struct {
		Repository struct {
			FullName string `json:"full_name"` //nolint:tagliatelle // GitHub uses this field name
		} `json:"repository"`
		Action string `json:"action"`
	}

	err := json.Unmarshal(body, &event)
	if err != nil {
		return "", "", fmt.Errorf("parse pull request: %w", err)
	}

	if event.Repository.FullName == "" || event.Action == "" {
		return "", "", errMissingFields
	}

	return event.Repository.FullName, event.Action, nil
}

func oneLine(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "") }
