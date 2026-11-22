// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package receiver accepts GitHub webhook deliveries. It authenticates each
// delivery and queues the body in the store before it answers 202.
package receiver

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/tbhb-dev/github-metrics/internal/store"
)

// MaxBody is GitHub's largest webhook payload, 25 MiB.
const MaxBody = 25 << 20

// MaxInFlight caps the deliveries whose bodies are being read at once. Each
// can buffer up to MaxBody, so this bounds the receiver's body memory at
// MaxInFlight times MaxBody, which is 400 MiB.
const MaxInFlight = 16

const signaturePrefix = "sha256="

// The signature covers only the body, so both headers need their own check.
// Event names are lowercase and can hold digits, as in projects_v2.
var (
	deliveryPattern = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	eventPattern    = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// Handler serves GET /healthz and POST /webhook. Every other request, and a
// non-POST request to /webhook, gets 404. A delivery that arrives while
// MaxInFlight others are being read gets 503 with Retry-After. It dispatches on the exact method
// and path, with no path cleaning, so "//webhook" is a 404 and not a redirect.
func Handler(secret []byte, repo store.Store, log *slog.Logger) http.Handler {
	slots := make(chan struct{}, MaxInFlight)

	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/healthz":
			resp.WriteHeader(http.StatusOK)
		case req.Method == http.MethodPost && req.URL.Path == "/webhook":
			webhook(resp, req, secret, repo, log, slots)
		default:
			resp.WriteHeader(http.StatusNotFound)
		}
	})
}

// webhook authenticates the delivery and queues the body in the store.
func webhook(
	resp http.ResponseWriter,
	req *http.Request,
	secret []byte,
	repo store.Store,
	log *slog.Logger,
	slots chan struct{},
) {
	want, ok := parseSignature(secret, req.Header.Get("X-Hub-Signature-256"))
	if !ok {
		reject(resp, req, log)

		return
	}

	event := req.Header.Get("X-Github-Event")

	delivery := req.Header.Get("X-Github-Delivery")

	if !deliveryPattern.MatchString(delivery) || !eventPattern.MatchString(event) {
		log.WarnContext(req.Context(), "rejected delivery", "reason", "bad delivery or event header")
		resp.WriteHeader(http.StatusBadRequest)

		return
	}

	if req.ContentLength > MaxBody {
		resp.WriteHeader(http.StatusRequestEntityTooLarge)

		return
	}

	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		log.WarnContext(req.Context(), "rejected delivery", "reason", "too many deliveries in flight")
		resp.Header().Set("Retry-After", "5")
		resp.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	var body bytes.Buffer

	mac := hmac.New(sha256.New, secret)

	size, err := io.Copy(mac, io.TeeReader(http.MaxBytesReader(resp, req.Body, MaxBody), &body))
	if err != nil {
		_, tooLarge := errors.AsType[*http.MaxBytesError](err)
		if tooLarge {
			resp.WriteHeader(http.StatusRequestEntityTooLarge)

			return
		}

		resp.WriteHeader(http.StatusBadRequest)

		return
	}

	if !hmac.Equal(want, mac.Sum(nil)) {
		reject(resp, req, log)

		return
	}

	outcome, err := repo.Enqueue(delivery, event, body.Bytes())
	if err != nil {
		log.ErrorContext(req.Context(), "enqueue delivery",
			"delivery", oneLine(delivery),
			"event", oneLine(event),
			"error", err)
		resp.WriteHeader(http.StatusInternalServerError)

		return
	}

	result := "enqueued"
	if outcome == store.Duplicate {
		result = "duplicate"
	}

	log.InfoContext(req.Context(), "accepted delivery",
		"delivery", delivery,
		"event", event,
		"bytes", size,
		"outcome", result)
	resp.WriteHeader(http.StatusAccepted)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "")

	return strings.ReplaceAll(s, "\r", "")
}

func reject(resp http.ResponseWriter, req *http.Request, log *slog.Logger) {
	log.WarnContext(req.Context(), "rejected delivery", "reason", "bad signature")
	resp.WriteHeader(http.StatusUnauthorized)
}

// parseSignature returns the digest in header, which must be "sha256=" and a
// full-length hex SHA-256 digest. It fails for an empty secret, so a missing
// secret never authenticates anything.
func parseSignature(secret []byte, header string) ([]byte, bool) {
	if len(secret) == 0 || !strings.HasPrefix(header, signaturePrefix) {
		return nil, false
	}

	digest, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil || len(digest) != sha256.Size {
		return nil, false
	}

	return digest, true
}
