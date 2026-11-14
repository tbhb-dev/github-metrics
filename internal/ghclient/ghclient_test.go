// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package ghclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
	"github.com/tbhb-dev/github-metrics/internal/ghclient"
)

var errNoKey = errors.New("no key")

// fakeTokens hands out numbered fake tokens and counts renewals.
type fakeTokens struct {
	renewals atomic.Int32
}

func newTokens() *fakeTokens {
	return &fakeTokens{renewals: atomic.Int32{}}
}

func (f *fakeTokens) Token(context.Context) (string, error) {
	return "fake-" + strconv.Itoa(int(f.renewals.Load())), nil
}

func (f *fakeTokens) Renew(context.Context) (string, error) {
	return "fake-" + strconv.Itoa(int(f.renewals.Add(1))), nil
}

type failingTokens struct{}

func (failingTokens) Token(context.Context) (string, error) { return "", errNoKey }
func (failingTokens) Renew(context.Context) (string, error) { return "", errNoKey }

func newClient(
	t *testing.T,
	handler http.HandlerFunc,
	tokens ghclient.TokenSource,
) (*ghclient.Client, *httptest.Server) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := ghclient.New(srv.URL, srv.Client(), budget.New(), tokens, ghclient.NewETags())
	if err != nil {
		t.Fatal(err)
	}

	return client, srv
}

func write(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()

	_, err := w.Write([]byte(body))
	if err != nil {
		t.Error(err)
	}
}

const etagValue = `"abc"`

func TestGetSendsHeaders(t *testing.T) {
	t.Parallel()

	client, _ := newClient(t, func(resp http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer fake-0" {
			t.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}

		if req.Header.Get("X-Github-Api-Version") == "" || req.Header.Get("Accept") == "" {
			t.Error("missing version or accept header")
		}

		write(t, resp, "[]")
	}, newTokens())

	res, err := client.Get(t.Context(), budget.Webhook, "/repos/o/r/pulls")
	if err != nil || res.NotModified() || string(res.Body) != "[]" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestConditionalRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	client, _ := newClient(t, func(resp http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			if req.Header.Get("If-None-Match") != "" {
				t.Error("first request sent If-None-Match")
			}

			resp.Header().Set("ETag", etagValue)
			write(t, resp, "[]")

			return
		}

		if req.Header.Get("If-None-Match") != etagValue {
			t.Errorf("If-None-Match = %q", req.Header.Get("If-None-Match"))
		}

		resp.WriteHeader(http.StatusNotModified)
	}, newTokens())

	first, err := client.Get(t.Context(), budget.Webhook, "/p")
	if err != nil || first.ETag != etagValue {
		t.Fatalf("first = %+v, %v", first, err)
	}

	second, err := client.Get(t.Context(), budget.Webhook, "/p")
	if err != nil || !second.NotModified() || second.ETag != etagValue || len(second.Body) != 0 {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestGetFollowsNextLink(t *testing.T) {
	t.Parallel()

	var base string

	client, srv := newClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+base+`/x?page=2>; rel="next", <`+base+`/x?page=9>; rel="last"`)
		}
	}, newTokens())
	base = srv.URL

	res, err := client.Get(t.Context(), budget.Webhook, "/x")
	if err != nil || res.Next != srv.URL+"/x?page=2" {
		t.Fatalf("next = %+v, %v", res, err)
	}

	last, err := client.Get(t.Context(), budget.Webhook, res.Next)
	if err != nil || last.Next != "" {
		t.Fatalf("last = %+v, %v", last, err)
	}
}

func TestGetRefusesOtherHost(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	client, _ := newClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, newTokens())

	_, err := client.Get(t.Context(), budget.Webhook, "http://127.0.0.1:1/steal")
	if !errors.Is(err, ghclient.ErrOutsideBase) || calls.Load() != 0 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestUnauthorizedRenewsOnce(t *testing.T) {
	t.Parallel()

	tokens := newTokens()
	client, _ := newClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") == "Bearer fake-0" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}, tokens)

	_, err := client.Get(t.Context(), budget.Webhook, "/x")
	if err != nil || tokens.renewals.Load() != 1 {
		t.Fatalf("err = %v, renewals = %d", err, tokens.renewals.Load())
	}
}

func TestSecondUnauthorizedStops(t *testing.T) {
	t.Parallel()

	tokens := newTokens()
	client, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, tokens)

	_, err := client.Get(t.Context(), budget.Webhook, "/x")

	var status *ghclient.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusUnauthorized || tokens.renewals.Load() != 1 {
		t.Fatalf("err = %v, renewals = %d", err, tokens.renewals.Load())
	}
}

func TestOtherStatusFails(t *testing.T) {
	t.Parallel()

	client, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}, newTokens())

	var status *ghclient.StatusError

	_, err := client.Get(t.Context(), budget.Webhook, "/x")
	if !errors.As(err, &status) || status.Status != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenErrorStops(t *testing.T) {
	t.Parallel()

	client, _ := newClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request sent without a token")
	}, failingTokens{})

	_, err := client.Get(t.Context(), budget.Webhook, "/x")
	if err == nil {
		t.Fatal("want an error")
	}
}

// roundTrip adapts a function to http.RoundTripper. The fake-clock tests use
// it because sockets aren't durably blocking inside a synctest bubble.
type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req), nil }

func reply(status int, header http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: header, Body: http.NoBody}
}

func fakeClient(t *testing.T, transport roundTrip) *ghclient.Client {
	t.Helper()

	client, err := ghclient.New(
		"http://api.test",
		&http.Client{Transport: transport, CheckRedirect: nil, Jar: nil, Timeout: 0},
		budget.New(),
		newTokens(),
		ghclient.NewETags(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return client
}

func TestSecondaryLimitBacksOffThenRetries(t *testing.T) {
	t.Parallel()

	synctestRun(t, func(ctx context.Context) {
		var calls atomic.Int32

		client := fakeClient(t, func(*http.Request) *http.Response {
			if calls.Add(1) == 1 {
				return reply(http.StatusForbidden, http.Header{"Retry-After": {"120"}})
			}

			return reply(http.StatusOK, http.Header{})
		})

		start := time.Now()

		_, err := client.Get(ctx, budget.Webhook, "/x")
		if err != nil || calls.Load() != 2 {
			t.Fatalf("err = %v, calls = %d", err, calls.Load())
		}

		if waited := time.Since(start); waited < 120*time.Second {
			t.Fatalf("waited %v, want at least the retry-after", waited)
		}
	})
}

func TestSecondaryLimitGivesUp(t *testing.T) {
	t.Parallel()

	synctestRun(t, func(ctx context.Context) {
		client := fakeClient(t, func(*http.Request) *http.Response {
			return reply(http.StatusTooManyRequests, http.Header{})
		})

		_, err := client.Get(ctx, budget.Webhook, "/x")
		if !errors.Is(err, ghclient.ErrGaveUp) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLowRemainingStopsUntilReset(t *testing.T) {
	t.Parallel()

	synctestRun(t, func(ctx context.Context) {
		var calls atomic.Int32

		reset := time.Now().Add(30 * time.Minute)

		client := fakeClient(t, func(*http.Request) *http.Response {
			header := http.Header{}
			if calls.Add(1) == 1 {
				header.Set("X-Ratelimit-Remaining", "10")
				header.Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			}

			return reply(http.StatusOK, header)
		})

		for range 2 {
			_, err := client.Get(ctx, budget.Webhook, "/x")
			if err != nil {
				t.Fatal(err)
			}
		}

		if time.Now().Before(reset) {
			t.Fatalf("second request ran at %v, before the reset %v", time.Now(), reset)
		}
	})
}
