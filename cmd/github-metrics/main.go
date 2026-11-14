// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Command github-metrics runs the webhook receiver.
//
// WEBHOOK_SECRET_FILE names a file that contains the App's webhook secret.
//
// LISTEN_ADDR is the listen address and defaults to :8080.
//
// With -healthcheck the command probes the local health endpoint and exits
// 0 or 1, so a container health check works in an image that has no shell.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/receiver"
)

// The write timeout covers the body read, so it must exceed the read timeout.
const (
	headerTimeout   = 5 * time.Second
	readTimeout     = 30 * time.Second
	writeTimeout    = 40 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 10 * time.Second
	probeTimeout    = 3 * time.Second
)

var (
	errNoSecretFile = errors.New("WEBHOOK_SECRET_FILE is not set")
	errEmptySecret  = errors.New("webhook secret file is empty")
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the health endpoint and exit")

	flag.Parse()

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	if *healthcheck {
		os.Exit(probe(addr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	err := run(addr, log)
	if err != nil {
		log.Error("exit", "error", err)
		os.Exit(1)
	}
}

func run(addr string, log *slog.Logger) error {
	file := os.Getenv("WEBHOOK_SECRET_FILE")
	if file == "" {
		return errNoSecretFile
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the operator names this path
	if err != nil {
		return fmt.Errorf("read webhook secret: %w", err)
	}

	secret := bytes.TrimRight(raw, "\r\n")
	if len(secret) == 0 {
		return errEmptySecret
	}

	srv := new(http.Server)
	srv.Addr = addr
	srv.Handler = receiver.Handler(secret, log)
	srv.ReadHeaderTimeout = headerTimeout
	srv.ReadTimeout = readTimeout
	srv.WriteTimeout = writeTimeout
	srv.IdleTimeout = idleTimeout

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)

	go func() { serveErr <- srv.ListenAndServe() }()

	log.Info("listening", "addr", addr)

	select {
	case err = <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		return fmt.Errorf("shut down: %w", err)
	}

	return nil
}

// probe asks the local health endpoint and returns the exit code.
func probe(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	url := "http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // loopback
	if err != nil {
		return 1
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // loopback
	if err != nil {
		return 1
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 1
	}

	return 0
}
