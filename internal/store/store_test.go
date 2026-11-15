// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/store"
)

func open(t *testing.T, path string) *store.Bolt {
	t.Helper()

	repo, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	return repo
}

func enqueue(t *testing.T, repo store.Store, delivery, body string) store.Outcome {
	t.Helper()

	outcome, err := repo.Enqueue(delivery, []byte(body))
	if err != nil {
		t.Fatalf("Enqueue(%q): %v", delivery, err)
	}

	return outcome
}

func pending(t *testing.T, repo store.Store) []store.Entry {
	t.Helper()

	entries, err := repo.Pending(100)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}

	return entries
}

func TestEnqueueThenPending(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))

	if got := enqueue(t, repo, "d1", `{"a":1}`); got != store.Enqueued {
		t.Fatalf("outcome = %v, want Enqueued", got)
	}

	entries := pending(t, repo)
	if len(entries) != 1 || entries[0].Delivery != "d1" || string(entries[0].Body) != `{"a":1}` {
		t.Fatalf("pending = %+v", entries)
	}
}

func TestDuplicateWritesNothing(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))
	enqueue(t, repo, "d1", "body")

	// The same body under a new ID is a duplicate, since the signature
	// covers only the body.
	if got := enqueue(t, repo, "d2", "body"); got != store.Duplicate {
		t.Fatalf("same body, new ID: outcome = %v, want Duplicate", got)
	}

	if got := enqueue(t, repo, "d1", "body"); got != store.Duplicate {
		t.Fatalf("same body, same ID: outcome = %v, want Duplicate", got)
	}

	if queued := pending(t, repo); len(queued) != 1 {
		t.Fatalf("queue holds %d entries, want 1", len(queued))
	}

	// d2 was never recorded, so it can still arrive with its own body.
	if got := enqueue(t, repo, "d2", "other"); got != store.Enqueued {
		t.Fatalf("d2 with a new body: outcome = %v, want Enqueued", got)
	}
}

func TestReusedIDWithDifferentBody(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))
	enqueue(t, repo, "d1", "first")

	_, err := repo.Enqueue("d1", []byte("second"))
	if !errors.Is(err, store.ErrDeliveryReused) {
		t.Fatalf("err = %v, want ErrDeliveryReused", err)
	}

	entries := pending(t, repo)
	if len(entries) != 1 || string(entries[0].Body) != "first" {
		t.Fatalf("pending = %+v, want only the first body", entries)
	}

	// The rejected body has no dedupe row, so it can arrive under a new ID.
	if got := enqueue(t, repo, "d2", "second"); got != store.Enqueued {
		t.Fatalf("rejected body under a new ID: outcome = %v, want Enqueued", got)
	}
}

func TestReplayAfterReopenAndRemove(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "store.db")
	repo := open(t, path)
	enqueue(t, repo, "d1", "body")

	entries := pending(t, repo)

	err := repo.Remove(entries[0].Seq)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err = repo.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// GitHub redelivers a body the worker already took off the queue. The
	// dedupe table remembers it across a restart.
	repo = open(t, path)
	if got := enqueue(t, repo, "d1-redelivery", "body"); got != store.Duplicate {
		t.Fatalf("replay: outcome = %v, want Duplicate", got)
	}

	if requeued := pending(t, repo); len(requeued) != 0 {
		t.Fatalf("replay re-queued %+v", requeued)
	}
}

func TestOutOfOrderPair(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))

	// A review arrives before the PR event that came first on GitHub's side.
	// The queue keeps arrival order, and the worker reconciles from REST.
	enqueue(t, repo, "review-delivery", "review")
	enqueue(t, repo, "opened-delivery", "opened")

	entries := pending(t, repo)
	if len(entries) != 2 || entries[0].Delivery != "review-delivery" || entries[1].Delivery != "opened-delivery" {
		t.Fatalf("pending = %+v, want arrival order", entries)
	}

	err := repo.Remove(entries[0].Seq)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err = repo.Remove(entries[0].Seq)
	if err != nil {
		t.Fatalf("second Remove of the same entry: %v", err)
	}

	left := pending(t, repo)
	if len(left) != 1 || left[0].Delivery != "opened-delivery" {
		t.Fatalf("after Remove, pending = %+v", left)
	}

	none, err := repo.Pending(0)
	if err != nil || len(none) != 0 {
		t.Fatalf("Pending(0) = %+v, %v", none, err)
	}
}

func TestFailedWriteLeavesStoreUnchanged(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))
	enqueue(t, repo, "d1", "kept")

	// `bbolt` rejects a key over 32768 bytes after the body hash row is
	// already written in the same transaction, so this fails mid-write.
	huge := strings.Repeat("x", 40000)

	_, err := repo.Enqueue(huge, []byte("lost"))
	if err == nil {
		t.Fatal("Enqueue with an oversized delivery ID succeeded")
	}

	entries := pending(t, repo)
	if len(entries) != 1 || string(entries[0].Body) != "kept" {
		t.Fatalf("queue changed: %+v", entries)
	}

	// The rollback removed the body row too, so the body is new again.
	if got := enqueue(t, repo, "d2", "lost"); got != store.Enqueued {
		t.Fatalf("body of the failed write: outcome = %v, want Enqueued", got)
	}
}

func TestEmptyDeliveryID(t *testing.T) {
	t.Parallel()

	repo := open(t, filepath.Join(t.TempDir(), "store.db"))

	_, err := repo.Enqueue("", []byte("body"))
	if !errors.Is(err, store.ErrNoDelivery) {
		t.Fatalf("err = %v, want ErrNoDelivery", err)
	}
}

func TestCreatedIsRecordedOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "store.db")
	before := time.Now()
	repo := open(t, path)
	after := time.Now()

	created := repo.Created()
	if created.Before(before.Truncate(time.Nanosecond)) || created.After(after) {
		t.Fatalf("Created = %v, want between %v and %v", created, before, after)
	}

	err := repo.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := open(t, path)
	if !again.Created().Equal(created) {
		t.Fatalf("Created after reopen = %v, want %v", again.Created(), created)
	}
}

func TestNewFileMode(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows reports every file as 0666, so the mode bits don't map there")
	}

	path := filepath.Join(t.TempDir(), "store.db")
	open(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
