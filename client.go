// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The client's built-in defaults, each overridable by an environment variable
// and then by an Option.
const (
	// DefaultBaseURL is the API root requests go to.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the model a Request that leaves Model empty is sent to.
	DefaultModel = "jev-latest"
	// DefaultTimeout bounds one attempt, retries excluded.
	DefaultTimeout = 10 * time.Second
)

// The environment variables the client reads. A variable that is unset, empty
// or whitespace-only is treated as absent.
const (
	// EnvAPIKey holds the API key used when none is passed to NewClient.
	EnvAPIKey = "TYPESAFE_API_KEY"
	// EnvBaseURL holds the API root used when WithBaseURL is not passed.
	EnvBaseURL = "TYPESAFE_BASE_URL"
	// EnvDefaultModel holds the default model used when WithDefaultModel is
	// not passed.
	EnvDefaultModel = "TYPESAFE_DEFAULT_MODEL"
)

// requestIDHeader names the response header that identifies the request in the
// server's own records.
const requestIDHeader = "x-typesafe-request-id"

// The API paths this client calls.
const (
	pathSystemOne = "/v1/systemone"
	pathModels    = "/v1/models"
)

// Client is a connection to the TypeSafe API. It is safe for concurrent use:
// every field is set at construction and only read afterwards.
type Client struct {
	apiKey       string
	baseURL      string
	defaultModel string
	userAgent    string
	timeout      time.Duration
	retry        RetryPolicy
	httpClient   *http.Client
	logger       *slog.Logger

	// now, sleep and randFloat are the client's contact with the clock and
	// with randomness. Tests replace them to run a retry schedule without
	// waiting on it.
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	randFloat func() float64
}

// NewClient builds a client.
//
// The API key is the one passed here; when that is empty or whitespace-only,
// the value of TYPESAFE_API_KEY is used instead. A client with no key at all
// is an error, because every request needs one.
//
// The API root is https://api.typesafe.ai, replaced by TYPESAFE_BASE_URL and
// then by WithBaseURL. The default model is jev-latest, replaced by
// TYPESAFE_DEFAULT_MODEL and then by WithDefaultModel.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	c := &Client{
		apiKey:       strings.TrimSpace(apiKey),
		baseURL:      envOr(EnvBaseURL, DefaultBaseURL),
		defaultModel: envOr(EnvDefaultModel, DefaultModel),
		userAgent:    defaultUserAgent,
		timeout:      DefaultTimeout,
		retry:        DefaultRetryPolicy(),
		httpClient:   defaultHTTPClient(),
		logger:       slog.New(slog.DiscardHandler),
		now:          time.Now,
		sleep:        sleepContext,
		randFloat:    rand.Float64,
	}
	if c.apiKey == "" {
		c.apiKey = env(EnvAPIKey)
	}
	if c.apiKey == "" {
		return nil, errors.New("typesafe: no API key; pass one to NewClient or set " + EnvAPIKey)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("typesafe: base URL %q: %w", c.baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("typesafe: base URL %q needs a scheme and a host", c.baseURL)
	}
	c.retry = c.retry.normalized()
	return c, nil
}

// defaultHTTPClient is the HTTP client used when WithHTTPClient is not passed.
// It sets no timeout of its own: the per-attempt deadline is a context, so
// that a timed-out attempt reports context.DeadlineExceeded.
func defaultHTTPClient() *http.Client {
	return &http.Client{Transport: http.DefaultTransport}
}

// env reads an environment variable, treating a whitespace-only value as
// absent.
func env(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// envOr reads an environment variable and falls back to a built-in default.
func envOr(name, fallback string) string {
	if value := env(name); value != "" {
		return value
	}
	return fallback
}

// Evaluate sends one state and its questions, and returns one answer per
// question under the ids the request used.
//
// The request is checked before it is sent; a request the API would reject on
// its shape comes back as a *ValidationError naming the field. A response
// outside 2xx comes back as an *APIError, after the client has exhausted its
// retry policy on the statuses worth retrying.
func (c *Client) Evaluate(ctx context.Context, req *Request) (*Response, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	body, err := encodeRequest(req, c.defaultModel)
	if err != nil {
		return nil, err
	}
	result, err := c.do(ctx, http.MethodPost, pathSystemOne, body)
	if err != nil {
		return nil, err
	}
	var response Response
	if err := json.Unmarshal(result.body, &response); err != nil {
		return nil, fmt.Errorf("typesafe: decoding the %s response: %w", pathSystemOne, err)
	}
	response.RequestID = result.requestID
	return &response, nil
}

// result is one completed HTTP exchange: the status, the request id the
// server assigned, the response headers, and the body read in full.
type result struct {
	status    int
	requestID string
	header    http.Header
	body      []byte
}

// do sends a request and retries it according to the client's policy. The
// error it returns is the last failure as it happened, either an *APIError or
// the transport error wrapped, so that errors.As and errors.Is both reach the
// cause instead of a summary of it.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*result, error) {
	template, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("typesafe: building the %s %s request: %w", method, path, err)
	}
	template.Header.Set("Authorization", "Bearer "+c.apiKey)
	template.Header.Set("Accept", "application/json")
	template.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		template.Header.Set("Content-Type", "application/json")
	}

	var last error
	for retry := 0; ; retry++ {
		res, err := c.attempt(ctx, template, body)
		var header http.Header
		switch {
		case err != nil:
			// A context the caller ended is not a failure to retry: the
			// caller asked for the work to stop.
			if ctx.Err() != nil {
				return nil, err
			}
			last = err
		case res.status >= 200 && res.status < 300:
			return res, nil
		default:
			apiErr := newAPIError(res.status, res.requestID, res.body)
			if !retryableStatus(res.status) {
				return nil, apiErr
			}
			last, header = apiErr, res.header
		}
		if retry >= c.retry.MaxRetries {
			return nil, last
		}
		delay := c.retryDelay(retry, header)
		c.logger.DebugContext(ctx, "typesafe: retrying a failed request",
			"method", method, "path", path, "retry", retry+1,
			"of", c.retry.MaxRetries, "delay", delay, "cause", last)
		if err := c.sleep(ctx, delay); err != nil {
			return nil, fmt.Errorf("typesafe: %s %s: %w", method, path, err)
		}
	}
}

// retryDelay is the computed backoff, replaced by the wait the server asked
// for when the response carried one that is no longer than MaxRetryAfter.
func (c *Client) retryDelay(retry int, header http.Header) time.Duration {
	delay := c.retry.backoff(retry, c.randFloat)
	if header == nil {
		return delay
	}
	if asked, ok := retryAfter(header, c.now()); ok && asked <= c.retry.MaxRetryAfter {
		return asked
	}
	return delay
}

// attempt sends the request once and reads the response in full, so that the
// connection is returned to the pool before the next retry waits on the clock.
//
// The body is rebuilt here rather than reused, which is what makes a retry
// replayable: each attempt reads the same bytes from a fresh reader.
func (c *Client) attempt(ctx context.Context, template *http.Request, body []byte) (*result, error) {
	attemptCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req := template.Clone(attemptCtx)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("typesafe: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A failure body is untrusted in size, so only a bounded prefix of it is
	// kept. A success body is the caller's data and is read whole.
	reader := io.Reader(resp.Body)
	failed := resp.StatusCode < 200 || resp.StatusCode >= 300
	if failed {
		reader = io.LimitReader(resp.Body, maxErrorBody)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("typesafe: reading the %s %s response: %w", req.Method, req.URL.Path, err)
	}
	if failed {
		// Whatever the limit left behind is drained so the connection can be
		// reused instead of being torn down.
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return &result{
		status:    resp.StatusCode,
		requestID: resp.Header.Get(requestIDHeader),
		header:    resp.Header,
		body:      data,
	}, nil
}
