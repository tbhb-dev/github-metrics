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

// tamper flips one hex digit of the digest in signature, at index pos, where
// a negative pos counts from the end.
func tamper(signature string, pos int) string {
	digits := []byte(strings.TrimPrefix(signature, "sha256="))
	if pos < 0 {
		pos += len(digits)
	}

	if digits[pos] == '0' {
		digits[pos] = '1'
	} else {
		digits[pos] = '0'
	}

	return "sha256=" + string(digits)
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
			name:   "missing event",
			body:   body,
			header: map[string]string{sigHeader: sign(testSecret, body), deliveryHeader: deliveryID},
			want:   http.StatusBadRequest,
		},
		{
			name: "event with a space",
			body: body,
			header: map[string]string{
				sigHeader: sign(testSecret, body), deliveryHeader: deliveryID, eventHeader: "pull request",
			},
			want: http.StatusBadRequest,
		},
		{
			name: "event over 64 characters",
			body: body,
			header: map[string]string{
				sigHeader: sign(testSecret, body), deliveryHeader: deliveryID, eventHeader: strings.Repeat("a", 65),
			},
			want: http.StatusBadRequest,
		},
		{
			name: "delivery id with a newline and text",
			body: body,
			header: map[string]string{
				sigHeader: sign(testSecret, body), deliveryHeader: "d-1 secret=x", eventHeader: "ping",
			},
			want: http.StatusBadRequest,
		},
		{
			name:   "too large",
			body:   strings.Repeat("a", receiver.MaxBody+1),
			header: map[string]string{sigHeader: sign(testSecret, body), deliveryHeader: deliveryID},
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

func TestBadSignature(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	good := sign(testSecret, body)
	tests := map[string]string{
		"bad signature and no delivery id": "", // set below
		"first digest byte differs":        tamper(good, 0),
		"last digest byte differs":         tamper(good, -1),
		"digest truncated":                 good[:len(good)-2],
		"digest extended":                  good + "00",
	}
	tests["bad signature and no delivery id"] = sign("other", body)

	for name, signature := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			header := map[string]string{sigHeader: signature}
			if name != "bad signature and no delivery id" {
				header[deliveryHeader] = deliveryID
			}

			got := do(t, testSecret, http.MethodPost, "/webhook", body, header)
			if got != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", got)
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
		{http.MethodPost, "//webhook", http.StatusNotFound},
		{http.MethodPost, "/webhook/", http.StatusNotFound},
		{http.MethodGet, "/webhook/../healthz", http.StatusNotFound},
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
