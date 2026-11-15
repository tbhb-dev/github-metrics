// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package receiver_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tbhb-dev/github-metrics/internal/receiver"
	"github.com/tbhb-dev/github-metrics/internal/store"
)

const (
	sigHeader      = "X-Hub-Signature-256"
	deliveryHeader = "X-Github-Delivery"
	eventHeader    = "X-Github-Event"
	testSecret     = "test-secret"
	deliveryID     = "d-1"
	pingEvent      = "ping"
	openedBody     = `{"action":"opened"}`
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

func openStore(t *testing.T) *store.Bolt {
	t.Helper()

	repo, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	return repo
}

func do(t *testing.T, secret, method, path, body string, header map[string]string) int {
	t.Helper()

	handler := receiver.Handler([]byte(secret), openStore(t), slog.New(slog.DiscardHandler))
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))

	for name, value := range header {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec.Code
}

func postDelivery(t *testing.T, handler http.Handler) int {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook", strings.NewReader(openedBody))
	req.Header.Set(sigHeader, sign(testSecret, openedBody))
	req.Header.Set(deliveryHeader, deliveryID)
	req.Header.Set(eventHeader, "pull_request")

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

func TestWebhookHeaders(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	const guid = "0b7e4f1a-6c1d-11f1-9c3e-2f4a8d1b7e55"

	tests := []struct {
		name     string
		delivery string
		event    string
		want     int
	}{
		{"real GUID", guid, "pull_request", http.StatusAccepted},
		{"event with digits", deliveryID, "projects_v2_status_update", http.StatusAccepted},
		{"delivery id of 64 characters", strings.Repeat("a", 64), pingEvent, http.StatusAccepted},
		{"event of 64 characters", deliveryID, strings.Repeat("a", 64), http.StatusAccepted},
		{"missing delivery id", "", pingEvent, http.StatusBadRequest},
		{"missing event", deliveryID, "", http.StatusBadRequest},
		{"event with a space", deliveryID, "pull request", http.StatusBadRequest},
		{"event over 64 characters", deliveryID, strings.Repeat("a", 65), http.StatusBadRequest},
		{"delivery id over 64 characters", strings.Repeat("a", 65), pingEvent, http.StatusBadRequest},
		{"delivery id with a space and text", "d-1 secret=x", pingEvent, http.StatusBadRequest},
		{"delivery id with an underscore", "d_1", pingEvent, http.StatusBadRequest},
		{"delivery id with non-ASCII text", "d-\u00e9", pingEvent, http.StatusBadRequest},
		{"event with uppercase", deliveryID, "Ping", http.StatusBadRequest},
		{"event with non-ASCII text", deliveryID, "pi\u00f1g", http.StatusBadRequest},
	}

	for _, spec := range tests {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()

			header := map[string]string{sigHeader: sign(testSecret, body)}
			if spec.delivery != "" {
				header[deliveryHeader] = spec.delivery
			}

			if spec.event != "" {
				header[eventHeader] = spec.event
			}

			got := do(t, testSecret, http.MethodPost, "/webhook", body, header)
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

func TestNewDeliveryQueued(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	repo := openStore(t)
	handler := receiver.Handler([]byte(testSecret), repo, slog.New(slog.NewJSONHandler(&logs, nil)))

	if got := postDelivery(t, handler); got != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", got)
	}

	entries, err := repo.Pending(10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if len(entries) != 1 || entries[0].Delivery != deliveryID || string(entries[0].Body) != openedBody {
		t.Fatalf("pending = %+v", entries)
	}

	if !strings.Contains(logs.String(), `"outcome":"enqueued"`) {
		t.Errorf("log = %s, want outcome enqueued", logs.String())
	}
}

func TestReplayDeliveryIsDuplicate(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	repo := openStore(t)
	handler := receiver.Handler([]byte(testSecret), repo, slog.New(slog.NewJSONHandler(&logs, nil)))

	if got := postDelivery(t, handler); got != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", got)
	}

	logs.Reset()

	if got := postDelivery(t, handler); got != http.StatusAccepted {
		t.Fatalf("replay status = %d, want 202", got)
	}

	entries, err := repo.Pending(10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("pending len = %d, want 1", len(entries))
	}

	if !strings.Contains(logs.String(), `"outcome":"duplicate"`) {
		t.Errorf("replay log = %s, want outcome duplicate", logs.String())
	}
}

func TestStoreErrorAnswers500(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	repo := openStore(t)

	err := repo.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	handler := receiver.Handler([]byte(testSecret), repo, slog.New(slog.NewJSONHandler(&logs, nil)))

	if got := postDelivery(t, handler); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", got)
	}

	if !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Errorf("log = %s, want an ERROR line", logs.String())
	}
}
