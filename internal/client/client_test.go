// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripperFunc adapts a function to http.RoundTripper for use in tests.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestClient(rt http.RoundTripper) *FoundryClient {
	return &FoundryClient{
		ProjectEndpoint: "https://test.example.com/api/projects/test",
		authMode:        AuthModeAPIKey,
		apiKey:          "test-key",
		httpClient:      &http.Client{Transport: rt},
	}
}

func newProbeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestWaitForProjectReady_RetriesUntilWriteWarms exercises the acceptance
// criterion from issue #1: the probe must keep polling while POST /files
// returns 404 "Project not found", and complete once the data plane returns
// any non-retryable response (e.g. 4xx validation error).
func TestWaitForProjectReady_RetriesUntilWriteWarms(t *testing.T) {
	t.Parallel()

	const coldIterations = 3
	var calls atomic.Int32
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST probe, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/files") {
			t.Errorf("expected probe against /files, got %s", r.URL.Path)
		}
		n := calls.Add(1)
		if n <= coldIterations {
			return newProbeResponse(http.StatusNotFound,
				`{"error":{"code":"NotFound","message":"Project not found"}}`), nil
		}
		// Warmed: malformed multipart fails validation before any creation.
		return newProbeResponse(http.StatusBadRequest,
			`{"error":{"message":"Required field 'purpose' is missing"}}`), nil
	})

	c := newTestClient(rt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.waitForProjectReadyOnce(ctx, 2*time.Second, time.Millisecond, 5*time.Millisecond); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}

	if got := calls.Load(); got != coldIterations+1 {
		t.Errorf("expected %d probes (cold + warm), got %d", coldIterations+1, got)
	}
}

func TestWaitForProjectReady_RetriesOn5xx(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n <= 2 {
			return newProbeResponse(http.StatusServiceUnavailable, "upstream busy"), nil
		}
		return newProbeResponse(http.StatusBadRequest, "validation error"), nil
	})

	c := newTestClient(rt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.waitForProjectReadyOnce(ctx, 2*time.Second, time.Millisecond, 5*time.Millisecond); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 probes, got %d", got)
	}
}

func TestWaitForProjectReady_RetriesOn401Until2xx(t *testing.T) {
	t.Parallel()

	// 401/403 represent RBAC propagation. The probe must keep polling
	// instead of bailing out with "ready" — that was the v0.5.x behavior
	// and the comment in WaitForProjectReady documents why.
	var calls atomic.Int32
	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n <= 2 {
			return newProbeResponse(http.StatusUnauthorized, "AAD propagating"), nil
		}
		return newProbeResponse(http.StatusBadRequest, "validation error"), nil
	})

	c := newTestClient(rt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.waitForProjectReadyOnce(ctx, 2*time.Second, time.Millisecond, 5*time.Millisecond); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 probes, got %d", got)
	}
}

func TestWaitForProjectReady_TimesOutWhileCold(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return newProbeResponse(http.StatusNotFound,
			`{"error":{"message":"Project not found"}}`), nil
	})

	c := newTestClient(rt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.waitForProjectReadyOnce(ctx, 50*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "not write-reachable") {
		t.Errorf("expected 'not write-reachable' in error, got %v", err)
	}
}

func TestWaitForProjectReady_HonorsCachedFlag(t *testing.T) {
	t.Parallel()

	// Once projectReady is set, WaitForProjectReady must short-circuit
	// without issuing any HTTP request — that's the per-session optimization
	// the public method documents.
	var calls atomic.Int32
	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return newProbeResponse(http.StatusBadRequest, "should not be called"), nil
	})

	c := newTestClient(rt)
	c.projectReady.Store(true)

	if err := c.WaitForProjectReady(context.Background(), 30*time.Minute); err != nil {
		t.Fatalf("expected nil from cached ready, got %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 HTTP calls when cached, got %d", got)
	}
}

func TestWaitForProjectReady_ZeroTimeoutSkips(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return newProbeResponse(http.StatusBadRequest, ""), nil
	})

	c := newTestClient(rt)
	if err := c.WaitForProjectReady(context.Background(), 0); err != nil {
		t.Fatalf("expected nil for zero timeout, got %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 HTTP calls for zero timeout, got %d", got)
	}
}
