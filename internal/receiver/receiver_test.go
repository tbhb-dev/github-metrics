// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package receiver_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
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
	sigHeader        = "X-Hub-Signature-256"
	deliveryHeader   = "X-Github-Delivery"
	eventHeader      = "X-Github-Event"
	testSecret       = "test-secret"
	deliveryID       = "d-1"
	pingEvent        = "ping"
	pullRequestEvent = "pull_request"
	openedBody       = `{"action":"opened"}`
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
	req.Header.Set(eventHeader, pullRequestEvent)

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
				sigHeader: sign(testSecret, body), deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
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
			name: "wrong secret",
			body: body,
			header: map[string]string{
				sigHeader: sign("other", body), deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "signature of another body",
			body: body,
			header: map[string]string{
				sigHeader: sign(testSecret, "x"), deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusUnauthorized,
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
			name: "too large",
			body: strings.Repeat("a", receiver.MaxBody+1),
			header: map[string]string{
				sigHeader: sign(testSecret, body), deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusRequestEntityTooLarge,
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
		{"real GUID", guid, pullRequestEvent, http.StatusAccepted},
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

const noDeliveryIDCase = "bad signature and no delivery id"

func TestBadSignature(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	good := sign(testSecret, body)
	tests := map[string]string{
		noDeliveryIDCase:            "", // set below
		"first digest byte differs": tamper(good, 0),
		"last digest byte differs":  tamper(good, -1),
		"digest truncated":          good[:len(good)-2],
		"digest extended":           good + "00",
	}
	tests[noDeliveryIDCase] = sign("other", body)

	for name, signature := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			header := map[string]string{sigHeader: signature, eventHeader: pullRequestEvent}
			if name != noDeliveryIDCase {
				header[deliveryHeader] = deliveryID
			}

			want := http.StatusUnauthorized
			if name == noDeliveryIDCase {
				// Header shape is checked before authentication, so no body is read.
				want = http.StatusBadRequest
			}

			got := do(t, testSecret, http.MethodPost, "/webhook", body, header)
			if got != want {
				t.Errorf("status = %d, want %d", got, want)
			}
		})
	}
}

type failReader struct {
	t *testing.T
}

func (r failReader) Read(_ []byte) (int, error) {
	r.t.Helper()
	r.t.Error("unexpected body read")

	return 0, io.EOF
}

func TestWebhookEarlyRejectReadsNoBody(t *testing.T) {
	t.Parallel()

	validSig := sign(testSecret, openedBody)

	tests := []struct {
		header        map[string]string
		name          string
		body          string
		contentLength int64
		want          int
	}{
		{
			name:          "missing signature",
			body:          "",
			contentLength: 0,
			header:        map[string]string{deliveryHeader: deliveryID, eventHeader: pullRequestEvent},
			want:          http.StatusUnauthorized,
		},
		{
			name:          "malformed signature",
			body:          "",
			contentLength: 0,
			header: map[string]string{
				sigHeader: "sha256=zz", deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusUnauthorized,
		},
		{
			name:          "missing event",
			body:          "",
			contentLength: 0,
			header:        map[string]string{sigHeader: validSig, deliveryHeader: deliveryID},
			want:          http.StatusBadRequest,
		},
		{
			name:          "missing delivery id",
			body:          "",
			contentLength: 0,
			header:        map[string]string{sigHeader: validSig, eventHeader: pullRequestEvent},
			want:          http.StatusBadRequest,
		},
		{
			name:          "malformed delivery id",
			body:          "",
			contentLength: 0,
			header: map[string]string{
				sigHeader: validSig, deliveryHeader: "not a delivery id!", eventHeader: pullRequestEvent,
			},
			want: http.StatusBadRequest,
		},
		{
			name:          "malformed event",
			body:          "",
			contentLength: 0,
			header: map[string]string{
				sigHeader: validSig, deliveryHeader: deliveryID, eventHeader: "pull request!",
			},
			want: http.StatusBadRequest,
		},
		{
			name:          "content length above max body",
			body:          "",
			contentLength: receiver.MaxBody + 1,
			header: map[string]string{
				sigHeader: validSig, deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusRequestEntityTooLarge,
		},
		{
			name:          "valid request",
			body:          openedBody,
			contentLength: int64(len(openedBody)),
			header: map[string]string{
				sigHeader: validSig, deliveryHeader: deliveryID, eventHeader: pullRequestEvent,
			},
			want: http.StatusAccepted,
		},
	}

	for _, spec := range tests {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			runEarlyReject(t, spec.header, spec.body, spec.contentLength, spec.want)
		})
	}
}

func runEarlyReject(t *testing.T, header map[string]string, body string, contentLength int64, want int) {
	t.Helper()

	var bodyReader io.Reader = failReader{t: t}
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	handler := receiver.Handler([]byte(testSecret), openStore(t), slog.New(slog.DiscardHandler))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook", bodyReader)
	req.ContentLength = contentLength

	for name, value := range header {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != want {
		t.Errorf("status = %d, want %d", rec.Code, want)
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

	if len(entries) != 1 || entries[0].Delivery != deliveryID || entries[0].Event != pullRequestEvent ||
		string(entries[0].Body) != openedBody {
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
