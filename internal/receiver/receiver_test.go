// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package receiver_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tbhb-dev/github-metrics/internal/receiver"
)

const (
	sigHeader      = "X-Hub-Signature-256"
	deliveryHeader = "X-Github-Delivery"
	eventHeader    = "X-Github-Event"
	testSecret     = "test-secret"
	deliveryID     = "d-1"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func do(t *testing.T, secret, method, path, body string, header map[string]string) int {
	t.Helper()

	handler := receiver.Handler([]byte(secret), slog.New(slog.DiscardHandler))
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))

	for name, value := range header {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec.Code
}

func TestWebhook(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	tests := []struct {
		header map[string]string
		name   string
		body   string
		want   int
	}{
		{
			name: "valid",
			body: body,
			header: map[string]string{
				sigHeader: sign(testSecret, body), deliveryHeader: deliveryID, eventHeader: "pull_request",
			},
			want: http.StatusAccepted,
		},
		{
			name:   "no signature",
			body:   body,
			header: map[string]string{deliveryHeader: deliveryID},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "wrong secret",
			body:   body,
			header: map[string]string{sigHeader: sign("other", body), deliveryHeader: deliveryID},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "signature of another body",
			body:   body,
			header: map[string]string{sigHeader: sign(testSecret, "x"), deliveryHeader: deliveryID},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "not hex",
			body:   body,
			header: map[string]string{sigHeader: "sha256=zz", deliveryHeader: deliveryID},
			want:   http.StatusUnauthorized,
		},
		{
			name: "wrong prefix",
			body: body,
			header: map[string]string{
				sigHeader:      "sha1=" + strings.TrimPrefix(sign(testSecret, body), "sha256="),
				deliveryHeader: deliveryID,
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "missing delivery id",
			body:   body,
			header: map[string]string{sigHeader: sign(testSecret, body)},
			want:   http.StatusBadRequest,
		},
		{
			name:   "too large",
			body:   strings.Repeat("a", receiver.MaxBody+1),
			header: map[string]string{sigHeader: "sha256=00", deliveryHeader: deliveryID},
			want:   http.StatusRequestEntityTooLarge,
		},
	}

	for _, spec := range tests {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()

			got := do(t, testSecret, http.MethodPost, "/webhook", spec.body, spec.header)
			if got != spec.want {
				t.Errorf("status = %d, want %d", got, spec.want)
			}
		})
	}
}

func TestEmptySecretRejects(t *testing.T) {
	t.Parallel()

	header := map[string]string{sigHeader: sign("", "x"), deliveryHeader: deliveryID}

	got := do(t, "", http.MethodPost, "/webhook", "x", header)
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/webhook", http.StatusNotFound},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, "/other", http.StatusNotFound},
		{http.MethodPost, "/healthz", http.StatusNotFound},
	}

	for _, spec := range tests {
		t.Run(spec.method+" "+spec.path, func(t *testing.T) {
			t.Parallel()

			got := do(t, testSecret, spec.method, spec.path, "", nil)
			if got != spec.want {
				t.Errorf("status = %d, want %d", got, spec.want)
			}
		})
	}
}
