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
	"strings"
)

// MaxBody is GitHub's largest webhook payload, 25 MiB.
const MaxBody = 25 << 20

const signaturePrefix = "sha256="

// Handler serves GET /healthz and POST /webhook. Every other request, and a
// non-POST request to /webhook, gets 404.
func Handler(secret []byte, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// "/" matches every method, so a wrong method on a known path is a 404
	// rather than the 405 that the multiplexer would send.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, req *http.Request) {
		webhook(w, req, secret, log)
	})

	return mux
}

func webhook(resp http.ResponseWriter, req *http.Request, secret []byte, log *slog.Logger) {
	body, err := io.ReadAll(http.MaxBytesReader(resp, req.Body, MaxBody))
	if err != nil {
		_, tooLarge := errors.AsType[*http.MaxBytesError](err)
		if tooLarge {
			resp.WriteHeader(http.StatusRequestEntityTooLarge)

			return
		}

		resp.WriteHeader(http.StatusBadRequest)

		return
	}

	// Authenticate before anything else reads a header or the body.
	if !validSignature(secret, body, req.Header.Get("X-Hub-Signature-256")) {
		log.WarnContext(req.Context(), "rejected delivery", "reason", "bad signature")
		resp.WriteHeader(http.StatusUnauthorized)

		return
	}

	delivery := req.Header.Get("X-Github-Delivery")
	if delivery == "" {
		resp.WriteHeader(http.StatusBadRequest)

		return
	}

	log.InfoContext(req.Context(), "accepted delivery",
		"delivery", delivery,
		"event", req.Header.Get("X-Github-Event"),
		"bytes", len(body))
	resp.WriteHeader(http.StatusAccepted)
}

// validSignature reports whether header is "sha256=" followed by the hex
// SHA-256 keyed hash of body under secret. An empty secret never validates.
func validSignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || !strings.HasPrefix(header, signaturePrefix) {
		return false
	}

	got, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)

	return hmac.Equal(got, mac.Sum(nil))
}
