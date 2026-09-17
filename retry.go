// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The retry defaults. They match the retry behaviour of the other TypeSafe
// clients: two retries, half a second of backoff doubling to a five second
// cap, and a refusal to wait longer than a minute on the server's say-so.
const (
	defaultMaxRetries     = 2
	defaultInitialBackoff = 500 * time.Millisecond
	defaultMaxBackoff     = 5 * time.Second
	defaultJitter         = 0.25
	defaultMaxRetryAfter  = 60 * time.Second
)

// RetryPolicy is how the client reacts to a failure it can retry: a request
// timeout, a rate limit, a server-side failure, a connection error, or an
// attempt that ran past the per-attempt timeout. A response the server refused
// on its merits, such as an invalid request or a rejected key, is never
// retried.
//
// The wait before retry n, counting the first retry as n = 0, is
//
//	delay_n = min(MaxBackoff, InitialBackoff * 2^n) * (1 - U(0, Jitter))
//
// where U(0, Jitter) is a uniform random fraction. Jitter therefore shortens
// the wait rather than lengthening it, and keeps clients that failed together
// from retrying together.
//
// A Retry-After or retry-after-ms header on the response replaces the computed
// delay, unless it asks for longer than MaxRetryAfter, in which case the
// computed delay is used instead.
//
// The zero value disables retries. Only MaxRetries carries meaning at zero;
// every other field falls back to its default when left at zero, so
// RetryPolicy{MaxRetries: 5} is five retries on the default schedule. Pass
// DefaultRetryPolicy as the starting point to change one field and keep the
// rest.
type RetryPolicy struct {
	// MaxRetries is how many times a failed request is sent again. Zero
	// disables retries.
	MaxRetries int
	// InitialBackoff is the wait before the first retry.
	InitialBackoff time.Duration
	// MaxBackoff caps the computed wait, however many retries have run.
	MaxBackoff time.Duration
	// Jitter is the largest fraction of the computed wait that is randomly
	// subtracted from it, from 0 for no jitter to 1 for a wait anywhere
	// between zero and the computed value.
	Jitter float64
	// MaxRetryAfter caps the wait the client will honour when the server asks
	// for one. A longer request falls back to the computed wait.
	MaxRetryAfter time.Duration
}

// DefaultRetryPolicy returns the policy a client uses when none is set.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     defaultMaxRetries,
		InitialBackoff: defaultInitialBackoff,
		MaxBackoff:     defaultMaxBackoff,
		Jitter:         defaultJitter,
		MaxRetryAfter:  defaultMaxRetryAfter,
	}
}

// normalized fills the fields whose zero value says nothing and clamps the
// ones that have a range. MaxRetries is left alone: zero there means no
// retries, which is a choice a caller can make.
func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxRetries < 0 {
		p.MaxRetries = 0
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = defaultInitialBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = defaultMaxBackoff
	}
	if p.MaxBackoff < p.InitialBackoff {
		p.MaxBackoff = p.InitialBackoff
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	if p.MaxRetryAfter <= 0 {
		p.MaxRetryAfter = defaultMaxRetryAfter
	}
	return p
}

// backoff computes the wait before retry number retry, counting from zero.
// The doubling runs as a loop rather than a shift so that a large retry count
// saturates at MaxBackoff instead of overflowing the duration.
func (p RetryPolicy) backoff(retry int, randFloat func() float64) time.Duration {
	delay := p.InitialBackoff
	for range retry {
		delay *= 2
		if delay >= p.MaxBackoff {
			break
		}
	}
	delay = min(delay, p.MaxBackoff)
	if p.Jitter > 0 {
		delay = time.Duration(float64(delay) * (1 - randFloat()*p.Jitter))
	}
	return max(delay, 0)
}

// retryableStatus reports whether a status is worth sending the request again
// for: a request timeout, a rate limit, or anything the server failed on,
// which covers the 529 overload status without naming it.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError
}

// retryAfter reads the wait the server asks for before the request is sent
// again. retry-after-ms is read first, because it is the more precise of the
// two, then Retry-After as either delta-seconds or an HTTP date. A header that
// is absent, unparseable or negative reports false, which leaves the computed
// backoff in place.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	if value := strings.TrimSpace(header.Get("retry-after-ms")); value != "" {
		if ms, err := strconv.ParseFloat(value, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(deadline.Sub(now), 0), true
	}
	return 0, false
}

// sleepContext waits for d, or until ctx ends, whichever comes first. It is
// the client's default waiter; tests replace it so that a retry schedule is
// exercised without spending the wall clock on it.
func sleepContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
