// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package worker consumes signed webhook bodies from the store queue.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/store"
)

const batchSize = 100

// Recorder records a pull request event, without a telemetry dependency.
type Recorder interface {
	RecordPullRequest(repository, action string)
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

// Pass drains batches until the queue is empty, the context ends, or the store fails.
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
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			w.record(ctx, entry)

			err = w.queue.Remove(entry.Seq)
			if err != nil {
				return fmt.Errorf("worker remove: %w", err)
			}
		}
	}

	return nil
}

func (w *Worker) record(ctx context.Context, entry store.Entry) {
	if entry.Event != "pull_request" {
		w.log.InfoContext(ctx, "discarded event", "event", oneLine(entry.Event))

		return
	}

	repository, action, err := parse(entry.Body)
	if err != nil {
		w.log.WarnContext(ctx, "discarded event", "reason", "bad pull request body")

		return
	}

	w.recorder.RecordPullRequest(repository, action)
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

	return event.Repository.FullName, event.Action, nil
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "")

	return strings.ReplaceAll(s, "\r", "")
}
