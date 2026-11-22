// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package appauth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentTokens(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		resp.WriteHeader(http.StatusCreated)

		_, _ = fmt.Fprintf(
			resp,
			`{"token":"installation-token","expires_at":%q}`,
			now.Add(time.Hour).Format(time.RFC3339),
		)
	}))
	t.Cleanup(server.Close)

	source := newSource(t, config(t, server, func() time.Time { return now }))
	concurrentCalls(t, source.Token)

	if calls.Load() != 1 {
		t.Error("concurrent calls didn't share the cache")
	}

	concurrentCalls(t, source.Renew)

	if calls.Load() != 33 {
		t.Error("concurrent Renew calls didn't each fetch a token")
	}
}

func concurrentCalls(t *testing.T, call func(context.Context) (string, error)) {
	t.Helper()

	start := make(chan struct{})

	var workers sync.WaitGroup

	for range 32 {
		workers.Go(func() {
			<-start

			token, err := call(t.Context())
			if err != nil || token != "installation-token" {
				t.Error("concurrent token call failed")
			}
		})
	}

	close(start)
	workers.Wait()
}
