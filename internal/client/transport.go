// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Transport / retry constants. Tuned for Foundry: hosted-agent cold starts
// can return 424 for tens of seconds, and ARM/RBAC propagation can produce
// transient 5xx during the first minutes of a project's life.
const (
	transportMaxRetries          = 4
	transportInitialBackoff      = 500 * time.Millisecond
	transportMaxBackoff          = 30 * time.Second
	transportMaxIdleConnsPerHost = 16
	transportIdleConnTimeout     = 90 * time.Second
	transportTLSHandshakeTimeout = 10 * time.Second
	transportExpectContinueWait  = 1 * time.Second
	httpClientTimeout            = 120 * time.Second
)

// newHTTPClient builds the http.Client used by FoundryClient. It wraps the
// default transport with retries on transient failures and tunes idle-conn
// reuse for the long-lived sessions a Terraform apply produces (many sequential
// requests to the same Foundry host).
func newHTTPClient() *http.Client {
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   transportMaxIdleConnsPerHost,
		IdleConnTimeout:       transportIdleConnTimeout,
		TLSHandshakeTimeout:   transportTLSHandshakeTimeout,
		ExpectContinueTimeout: transportExpectContinueWait,
	}
	return &http.Client{
		Timeout:   httpClientTimeout,
		Transport: &retryTransport{base: base, maxRetries: transportMaxRetries},
	}
}

// retryTransport retries transient failures (network errors and select 4xx/5xx)
// with exponential backoff + jitter. It honors the Retry-After header when the
// server provides one, and stops immediately if the request context is done.
//
// Bodies are buffered on first attempt so they can be replayed; this keeps the
// retry transparent to callers that pass *bytes.Reader / *strings.Reader.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := snapshotBody(req)
	if err != nil {
		return nil, err
	}

	backoff := transportInitialBackoff
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			restoreBody(req, body)
		}

		resp, err := t.base.RoundTrip(req)
		lastResp, lastErr = resp, err

		if !shouldRetry(req.Method, resp, err) || attempt >= t.maxRetries {
			return resp, err
		}

		// Drain & close before retry so the connection can be reused.
		if resp != nil {
			closeBody(resp)
			lastResp = nil
		}

		wait := backoff + jitter(backoff)
		if resp != nil {
			if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
				wait = d
			}
		}
		if wait > transportMaxBackoff {
			wait = transportMaxBackoff
		}

		select {
		case <-req.Context().Done():
			return lastResp, lastErr
		case <-time.After(wait):
		}

		backoff *= 2
		if backoff > transportMaxBackoff {
			backoff = transportMaxBackoff
		}
	}
}

// snapshotBody reads the request body so it can be replayed on retry.
// Returns nil for bodyless requests. Honors GetBody (set automatically by
// http.NewRequest when the body is *bytes.Buffer / *bytes.Reader / *strings.Reader)
// and falls back to a one-time read for arbitrary io.Readers.
func snapshotBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	if req.GetBody != nil {
		return nil, nil // already replayable; restoreBody will use GetBody
	}
	buf, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

func restoreBody(req *http.Request, snapshot []byte) {
	switch {
	case req.GetBody != nil:
		if b, err := req.GetBody(); err == nil {
			req.Body = b
		}
	case snapshot != nil:
		req.Body = io.NopCloser(bytes.NewReader(snapshot))
	}
}

// shouldRetry decides whether a (resp, err) pair is worth another attempt.
// We retry:
//   - transport errors that look network-y (timeout, reset, EOF mid-body)
//   - 408 Request Timeout, 425 Too Early, 429 Too Many Requests
//   - 5xx (502/503/504 most common; 500 included because Foundry occasionally
//     returns it during project warmup)
//
// We never retry on a canceled context — the outer caller has already given up.
func shouldRetry(method string, resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		var netErr net.Error
		if errors.As(err, &netErr) {
			return true
		}
		// Unwrapped EOFs and connection-reset-by-peer surface here too;
		// for idempotent methods always retry, and for POST retry the
		// transport-level failures since the request likely never landed.
		_ = method
		return true
	}
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// parseRetryAfter handles both forms allowed by RFC 7231 §7.1.3:
// integer seconds ("120") and HTTP-date.
func parseRetryAfter(h string) (time.Duration, bool) {
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// jitter returns a random duration in [0, d/2) to spread out concurrent
// retries. Uses crypto/rand because gosec G404 (rightly) flags math/rand for
// any production code path; the cost of one syscall per retry attempt is
// trivial against an HTTP request, and this lets the codebase stay nolint-free.
// On the rare error from rand.Read we fall back to no jitter — the retry still
// works, it just may collide more readily under load.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	n := int64(binary.BigEndian.Uint64(buf[:]) & ((1 << 63) - 1))
	return time.Duration(n % int64(d/2))
}
