// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package ghclient sends read requests to the GitHub REST API. Each request
// waits for a slot in the shared budget. It sends an installation token from a
// TokenSource and the stored `ETag`. The response's rate limit headers go back
// to the budget.
package ghclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tbhb-dev/github-metrics/internal/budget"
)

const (
	// maxAttempts bounds the sends of one Get, counting a send after a token
	// renewal and each retry after a secondary rate limit wait.
	maxAttempts = 4
	// maxBody caps a response body.
	maxBody = 16 << 20

	apiVersion = "2022-11-28"
	userAgent  = "github-metrics"
)

// ErrGaveUp is wrapped by the error of a Get that used every attempt.
var ErrGaveUp = errors.New("github request gave up")

// ErrOutsideBase is wrapped by the error of a Get whose target is on another
// host than the base URL, which would send the token there.
var ErrOutsideBase = errors.New("github request target is outside the base URL")

// TokenSource supplies installation tokens. The client never logs one, and
// never keeps one past a call.
type TokenSource interface {
	// Token returns the current token, renewed when little time remains.
	Token(ctx context.Context) (string, error)
	// Renew discards the current token and returns a new one. The client
	// calls it once after a 401 response.
	Renew(ctx context.Context) (string, error)
}

// StatusError reports a response the client doesn't retry and can't use.
type StatusError struct {
	Path   string
	Status int
}

// Error returns the path and status.
func (e *StatusError) Error() string {
	return fmt.Sprintf("github %s returned %d", e.Path, e.Status)
}

// ETags remembers the last `ETag` seen for each URL.
type ETags struct {
	seen map[string]string
	mu   sync.Mutex
}

// NewETags returns an empty store.
func NewETags() *ETags {
	return &ETags{seen: map[string]string{}, mu: sync.Mutex{}}
}

// Get returns the stored `ETag` for a URL, or an empty string.
func (e *ETags) Get(target string) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.seen[target]
}

// Set stores an `ETag` for a URL.
func (e *ETags) Set(target, etag string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.seen[target] = etag
}

// Response is a successful or not-modified answer.
type Response struct {
	// ETag is the response's `ETag`, or the stored one after a 304.
	ETag string
	// Next is the URL of the next page, or empty on the last page.
	Next string
	// Body is empty after a 304.
	Body []byte
	// Status is 200 or 304.
	Status int
}

// NotModified reports whether the stored `ETag` still matched.
func (r *Response) NotModified() bool {
	return r.Status == http.StatusNotModified
}

// Client sends the requests. Make one with New.
type Client struct {
	base   *url.URL
	http   *http.Client
	budget *budget.Limiter
	tokens TokenSource
	etags  *ETags
}

// New returns a client for the API at baseURL.
func New(
	baseURL string,
	httpClient *http.Client,
	lim *budget.Limiter,
	tokens TokenSource,
	etags *ETags,
) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}

	return &Client{base: base, http: httpClient, budget: lim, tokens: tokens, etags: etags}, nil
}

// Get fetches a path such as `/repos/o/r/pulls?per_page=100`, or the Next
// URL of an earlier response. It waits for a budget slot at the given
// priority. A 401 renews the token once. A secondary rate limit response
// backs the budget off and retries, up to four sends in all.
func (c *Client) Get(ctx context.Context, priority budget.Priority, target string) (*Response, error) {
	full, err := c.resolve(target)
	if err != nil {
		return nil, err
	}

	renew := false
	renewed := false

	for range maxAttempts {
		waitErr := c.budget.Wait(ctx, priority)
		if waitErr != nil {
			return nil, fmt.Errorf("get %s: %w", full.Path, waitErr)
		}

		token, tokenErr := c.token(ctx, renew)
		if tokenErr != nil {
			return nil, tokenErr
		}

		renewed = renewed || renew
		renew = false

		got, sendErr := c.send(ctx, full, token)
		if sendErr != nil {
			return nil, sendErr
		}

		c.observe(got.header)

		switch {
		case got.status == http.StatusOK || got.status == http.StatusNotModified:
			c.budget.Success()

			return c.response(full.String(), got), nil
		case got.status == http.StatusUnauthorized && !renewed:
			renew = true
		case isSecondaryLimit(got):
			c.budget.Backoff(retryAfter(got.header))
		default:
			return nil, &StatusError{Path: full.Path, Status: got.status}
		}
	}

	return nil, fmt.Errorf("get %s: %w", full.Path, ErrGaveUp)
}

// reply is what one send received.
type reply struct {
	header http.Header
	body   []byte
	status int
}

// resolve turns a path or a Next URL into a URL on the base host.
func (c *Client) resolve(target string) (*url.URL, error) {
	ref, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse target: %w", err)
	}

	full := c.base.ResolveReference(ref)
	if full.Scheme != c.base.Scheme || full.Host != c.base.Host {
		return nil, fmt.Errorf("get %s: %w", full.Host, ErrOutsideBase)
	}

	return full, nil
}

func (c *Client) token(ctx context.Context, renew bool) (string, error) {
	var (
		token string
		err   error
	)

	if renew {
		token, err = c.tokens.Renew(ctx)
	} else {
		token, err = c.tokens.Token(ctx)
	}

	if err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}

	return token, nil
}

func (c *Client) send(ctx context.Context, full *url.URL, token string) (*reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)

	if etag := c.etags.Get(full.String()); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", full.Path, err)
	}

	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", full.Path, err)
	}

	return &reply{header: res.Header, body: body, status: res.StatusCode}, nil
}

// observe passes the rate limit headers to the budget.
func (c *Client) observe(header http.Header) {
	remaining, err := strconv.Atoi(header.Get("X-Ratelimit-Remaining"))
	if err != nil {
		return
	}

	reset, err := strconv.ParseInt(header.Get("X-Ratelimit-Reset"), 10, 64)
	if err != nil {
		return
	}

	c.budget.Observe(remaining, time.Unix(reset, 0))
}

// response builds the result of a 200 or 304 and stores a new `ETag`.
func (c *Client) response(key string, got *reply) *Response {
	etag := got.header.Get("ETag")

	switch {
	case got.status == http.StatusNotModified:
		etag = c.etags.Get(key)
	case etag != "":
		c.etags.Set(key, etag)
	}

	return &Response{ETag: etag, Next: nextLink(got.header), Body: got.body, Status: got.status}
}

// isSecondaryLimit matches a 429, and a 403 that mentions a secondary limit or
// has a `retry-after` header.
func isSecondaryLimit(got *reply) bool {
	if got.status == http.StatusTooManyRequests {
		return true
	}

	return got.status == http.StatusForbidden &&
		(got.header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(string(got.body)), "secondary rate limit"))
}

// retryAfter returns the `retry-after` seconds, or zero when absent.
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// nextLink returns the `rel="next"` URL of a Link header.
func nextLink(header http.Header) string {
	for entry := range strings.SplitSeq(header.Get("Link"), ",") {
		target, rel, found := strings.Cut(strings.TrimSpace(entry), ";")
		if found && strings.Contains(rel, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(target), "<>")
		}
	}

	return ""
}
