// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package appauth_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/appauth"
)

type credentialError struct{}

func (credentialError) Error() string { return "credential-marker" }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, credentialError{} }

func (brokenBody) Close() error { return nil }

type brokenTransport struct {
	read bool
}

func (transport brokenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if !transport.read {
		return nil, credentialError{}
	}

	return &http.Response{StatusCode: http.StatusCreated, Body: brokenBody{}}, nil
}

func TestTransportFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	for _, read := range []bool{false, true} {
		stage := "send"
		status := 0

		if read {
			stage = "read"
			status = http.StatusCreated
		}

		t.Run(stage, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			cfg := config(t, server, time.Now)
			cfg.HTTPClient.Transport = brokenTransport{read: read}
			cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			source := newSource(t, cfg)

			checkFailure(t, source, &logs, stage, status)
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	t.Parallel()

	server := rejectingAPI(t)

	cfg := config(t, server, time.Now)
	tests := map[string]func(*appauth.Config){
		"empty base":            func(cfg *appauth.Config) { cfg.BaseURL = "" },
		"invalid base":          func(cfg *appauth.Config) { cfg.BaseURL += "%" },
		"relative base":         func(cfg *appauth.Config) { cfg.BaseURL = "/api/v3" },
		"query":                 func(cfg *appauth.Config) { cfg.BaseURL += "?credential-marker" },
		"empty query":           func(cfg *appauth.Config) { cfg.BaseURL += "?" },
		"fragment":              func(cfg *appauth.Config) { cfg.BaseURL += "#credential-marker" },
		"zero app":              func(cfg *appauth.Config) { cfg.AppID = 0 },
		"negative installation": func(cfg *appauth.Config) { cfg.InstallationID = -1 },
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			own := cfg
			own.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

			change(&own)

			source, err := appauth.New(own)
			if source != nil {
				t.Error("New accepted an invalid configuration")
			}

			checkAuthError(t, err)
			checkLog(t, &logs, "config", 0)
		})
	}
}
