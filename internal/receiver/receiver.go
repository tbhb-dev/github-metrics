// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package receiver accepts GitHub webhook deliveries. It authenticates each
// delivery and answers 202. It stores and processes nothing yet.
package receiver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// MaxBody is GitHub's largest webhook payload, 25 MiB.
const MaxBody = 25 << 20

const signaturePrefix = "sha256="

// The HMAC covers only the body, so a replayed valid body can arrive with any
// header text. The delivery ID and event name reach the logs, so both must
// match these patterns first. GitHub sends a UUID and a lowercase event name.
var (
	deliveryPattern = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	eventPattern    = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

// Handler serves GET /healthz and POST /webhook. Every other request, and a
// non-POST request to /webhook, gets 404. It dispatches on the exact method
// and path, with no path cleaning, so "//webhook" is a 404 and not a redirect.
func Handler(secret []byte, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/healthz":
			resp.WriteHeader(http.StatusOK)
		case req.Method == http.MethodPost && req.URL.Path == "/webhook":
			webhook(resp, req, secret, log)
		default:
			resp.WriteHeader(http.StatusNotFound)
		}
	})
}

// webhook authenticates the delivery without buffering it. The body streams
// into the hash and is discarded, which holds an unauthenticated request to a
// constant amount of memory.
func webhook(resp http.ResponseWriter, req *http.Request, secret []byte, log *slog.Logger) {
	want, ok := parseSignature(secret, req.Header.Get("X-Hub-Signature-256"))
	if !ok {
		reject(resp, req, log)

		return
	}

	mac := hmac.New(sha256.New, secret)

	size, err := io.Copy(mac, http.MaxBytesReader(resp, req.Body, MaxBody))
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

	delivery := req.Header.Get("X-Github-Delivery")
	event := req.Header.Get("X-Github-Event")

	if !deliveryPattern.MatchString(delivery) || !eventPattern.MatchString(event) {
		log.WarnContext(req.Context(), "rejected delivery", "reason", "bad delivery or event header")
		resp.WriteHeader(http.StatusBadRequest)

		return
	}

	log.InfoContext(req.Context(), "accepted delivery",
		"delivery", delivery,
		"event", event,
		"bytes", size)
	resp.WriteHeader(http.StatusAccepted)
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
