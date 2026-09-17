// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"
)

// noJitter is the jitter source a test uses when it wants the computed
// backoff exactly as the formula gives it.
func noJitter() float64 { return 0 }

func TestDefaultRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()
	want := RetryPolicy{
		MaxRetries:     2,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		Jitter:         0.25,
		MaxRetryAfter:  60 * time.Second,
	}
	if policy != want {
		t.Errorf("DefaultRetryPolicy() = %+v, want %+v", policy, want)
	}
}

func TestRetryPolicyNormalized(t *testing.T) {
	tests := []struct {
		name   string
		policy RetryPolicy
		want   RetryPolicy
	}{
		{
			name:   "the zero value keeps no retries and takes every other default",
			policy: RetryPolicy{},
			want: RetryPolicy{
				MaxRetries:     0,
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
				Jitter:         0,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name:   "a retry count on its own takes the default schedule",
			policy: RetryPolicy{MaxRetries: 5},
			want: RetryPolicy{
				MaxRetries:     5,
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
				Jitter:         0,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name:   "a negative retry count is no retries",
			policy: RetryPolicy{MaxRetries: -3},
			want: RetryPolicy{
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name:   "jitter is clamped to the unit interval",
			policy: RetryPolicy{Jitter: 4},
			want: RetryPolicy{
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
				Jitter:         1,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name:   "negative jitter is no jitter",
			policy: RetryPolicy{Jitter: -1},
			want: RetryPolicy{
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name:   "a cap below the first backoff is raised to it",
			policy: RetryPolicy{InitialBackoff: time.Second, MaxBackoff: time.Millisecond},
			want: RetryPolicy{
				InitialBackoff: time.Second,
				MaxBackoff:     time.Second,
				MaxRetryAfter:  defaultMaxRetryAfter,
			},
		},
		{
			name: "a fully set policy is left alone",
			policy: RetryPolicy{
				MaxRetries:     7,
				InitialBackoff: time.Second,
				MaxBackoff:     time.Minute,
				Jitter:         0.5,
				MaxRetryAfter:  time.Hour,
			},
			want: RetryPolicy{
				MaxRetries:     7,
				InitialBackoff: time.Second,
				MaxBackoff:     time.Minute,
				Jitter:         0.5,
				MaxRetryAfter:  time.Hour,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.normalized(); got != tt.want {
				t.Errorf("normalized() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRetryPolicyBackoffDoublesAndSaturates(t *testing.T) {
	policy := RetryPolicy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for retry, expected := range want {
		if got := policy.backoff(retry, noJitter); got != expected {
			t.Errorf("backoff(%d) = %v, want %v", retry, got, expected)
		}
	}
}

// A retry count large enough to overflow a duration if it were shifted has to
// saturate at the cap instead.
func TestRetryPolicyBackoffDoesNotOverflow(t *testing.T) {
	policy := RetryPolicy{InitialBackoff: time.Second, MaxBackoff: 5 * time.Second}
	if got := policy.backoff(1000, noJitter); got != 5*time.Second {
		t.Errorf("backoff(1000) = %v, want the cap", got)
	}
}

func TestRetryPolicyBackoffJitter(t *testing.T) {
	policy := RetryPolicy{InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: 0.25}
	tests := []struct {
		name   string
		random float64
		want   time.Duration
	}{
		{name: "no jitter drawn", random: 0, want: time.Second},
		{name: "half the jitter drawn", random: 0.5, want: 875 * time.Millisecond},
		{name: "the whole jitter drawn", random: 1, want: 750 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.backoff(0, func() float64 { return tt.random })
			if got != tt.want {
				t.Errorf("backoff = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryableStatus(t *testing.T) {
	retryable := []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		statusOverloaded,
	}
	for _, status := range retryable {
		if !retryableStatus(status) {
			t.Errorf("retryableStatus(%d) = false, want true", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 405, 422} {
		if retryableStatus(status) {
			t.Errorf("retryableStatus(%d) = true, want false", status)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{name: "no headers", header: http.Header{}},
		{
			name:   "milliseconds win over seconds",
			header: http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"9"}},
			want:   1500 * time.Millisecond,
			ok:     true,
		},
		{
			name:   "fractional milliseconds",
			header: http.Header{"Retry-After-Ms": {"250.5"}},
			want:   250500 * time.Microsecond,
			ok:     true,
		},
		{
			name:   "unparseable milliseconds fall through to seconds",
			header: http.Header{"Retry-After-Ms": {"soon"}, "Retry-After": {"3"}},
			want:   3 * time.Second,
			ok:     true,
		},
		{
			name:   "negative milliseconds fall through to seconds",
			header: http.Header{"Retry-After-Ms": {"-1"}, "Retry-After": {"3"}},
			want:   3 * time.Second,
			ok:     true,
		},
		{
			name:   "delta seconds",
			header: http.Header{"Retry-After": {" 2 "}},
			want:   2 * time.Second,
			ok:     true,
		},
		{
			name:   "negative delta seconds are ignored",
			header: http.Header{"Retry-After": {"-2"}},
		},
		{
			name:   "an http date in the future",
			header: http.Header{"Retry-After": {"Thu, 17 Sep 2026 12:00:30 GMT"}},
			want:   30 * time.Second,
			ok:     true,
		},
		{
			name:   "an http date in the past is no wait at all",
			header: http.Header{"Retry-After": {"Thu, 17 Sep 2026 11:59:00 GMT"}},
			want:   0,
			ok:     true,
		},
		{
			name:   "milliseconds beyond the duration range saturate",
			header: http.Header{"Retry-After-Ms": {"1e300"}},
			want:   math.MaxInt64,
			ok:     true,
		},
		{
			name:   "infinite seconds saturate",
			header: http.Header{"Retry-After": {"+Inf"}},
			want:   math.MaxInt64,
			ok:     true,
		},
		{
			name:   "not a number is ignored",
			header: http.Header{"Retry-After-Ms": {"NaN"}, "Retry-After": {"NaN"}},
		},
		{
			name:   "an unparseable value is ignored",
			header: http.Header{"Retry-After": {"whenever"}},
		},
		{
			name:   "an empty value is ignored",
			header: http.Header{"Retry-After": {"   "}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := retryAfter(tt.header, now)
			if ok != tt.ok {
				t.Fatalf("retryAfter reported ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("retryAfter = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSleepContext(t *testing.T) {
	ctx := t.Context()
	if err := sleepContext(ctx, 0); err != nil {
		t.Errorf("a zero wait returned %v", err)
	}
	if err := sleepContext(ctx, time.Millisecond); err != nil {
		t.Errorf("a short wait returned %v", err)
	}
}

func TestSleepContextStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepContext on a cancelled context = %v, want context.Canceled", err)
	}
	// A context cancelled while the timer runs ends the wait just the same.
	ctx, cancel = context.WithCancel(t.Context())
	go cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepContext interrupted = %v, want context.Canceled", err)
	}
}
