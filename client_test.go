// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder collects the waits a client asked for instead of spending them, so
// a retry schedule is checked without the clock taking part.
type recorder struct {
	mu    sync.Mutex
	waits []time.Duration
	err   error
}

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
	if r.err != nil {
		return r.err
	}
	return ctx.Err()
}

func (r *recorder) recorded() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.waits...)
}

// newTestClient builds a client pointed at a test server, with the clock and
// the jitter source pinned so a run is repeatable.
func newTestClient(t *testing.T, baseURL string, opts ...Option) (*Client, *recorder) {
	t.Helper()
	waits := &recorder{}
	opts = append([]Option{WithBaseURL(baseURL)}, opts...)
	c, err := NewClient("test-key", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.sleep = waits.sleep
	c.randFloat = noJitter
	c.now = func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }
	return c, waits
}

// clearEnv removes the client's environment variables for the duration of a
// test, so a machine that has them set does not change the result.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{EnvAPIKey, EnvBaseURL, EnvDefaultModel} {
		t.Setenv(name, "")
	}
}

// stall holds a handler open until the client gives up on the request or the
// test releases it. The request body is drained first: an HTTP/1 server only
// starts watching for a disconnect once the body is consumed, so a handler
// that skips the read never learns that the client walked away.
func stall(r *http.Request, release <-chan struct{}) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-release:
	}
}

// sampleRequest is a valid request used wherever the request itself is not
// what is under test.
func sampleRequest() *Request {
	return &Request{
		State:     "Help! My payouts have been failing for 3 days.",
		Questions: map[string]Question{"is_urgent": Noul{Instructions: "Does this convey urgency?"}},
	}
}

func TestNewClientAPIKey(t *testing.T) {
	t.Run("argument wins", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(EnvAPIKey, "from-env")
		c, err := NewClient("  from-arg  ")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if c.apiKey != "from-arg" {
			t.Errorf("apiKey = %q, want from-arg", c.apiKey)
		}
	})
	t.Run("empty argument falls back to the environment", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(EnvAPIKey, "  from-env  ")
		c, err := NewClient("")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if c.apiKey != "from-env" {
			t.Errorf("apiKey = %q, want from-env", c.apiKey)
		}
	})
	t.Run("whitespace argument falls back to the environment", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(EnvAPIKey, "from-env")
		c, err := NewClient("   ")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if c.apiKey != "from-env" {
			t.Errorf("apiKey = %q, want from-env", c.apiKey)
		}
	})
	t.Run("no key at all", func(t *testing.T) {
		clearEnv(t)
		if _, err := NewClient(""); err == nil {
			t.Fatal("NewClient with no key should fail")
		}
	})
	t.Run("whitespace-only environment value is not a key", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(EnvAPIKey, "   \t ")
		if _, err := NewClient(""); err == nil {
			t.Fatal("NewClient with a whitespace-only key should fail")
		}
	})
}

func TestNewClientDefaults(t *testing.T) {
	clearEnv(t)
	c, err := NewClient("k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if c.defaultModel != DefaultModel {
		t.Errorf("defaultModel = %q, want %q", c.defaultModel, DefaultModel)
	}
	if c.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", c.timeout, DefaultTimeout)
	}
	if c.userAgent != "typesafe-ai-go-sdk/"+Version {
		t.Errorf("userAgent = %q", c.userAgent)
	}
	if c.retry != DefaultRetryPolicy() {
		t.Errorf("retry = %+v, want the default policy", c.retry)
	}
	if c.httpClient == nil || c.httpClient.Transport == nil {
		t.Error("the default HTTP client carries no transport")
	}
	if c.logger == nil {
		t.Error("the default logger is nil")
	}
}

func TestNewClientReadsTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvBaseURL, "  https://staging.example.com/  ")
	t.Setenv(EnvDefaultModel, "  jev-preview  ")
	c, err := NewClient("k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != "https://staging.example.com" {
		t.Errorf("baseURL = %q, want the trimmed value with no trailing slash", c.baseURL)
	}
	if c.defaultModel != "jev-preview" {
		t.Errorf("defaultModel = %q, want jev-preview", c.defaultModel)
	}
}

func TestNewClientIgnoresWhitespaceOnlyEnvironmentValues(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvBaseURL, "   ")
	t.Setenv(EnvDefaultModel, "  \t")
	c, err := NewClient("k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != DefaultBaseURL || c.defaultModel != DefaultModel {
		t.Errorf("baseURL = %q, defaultModel = %q, want the built-in defaults", c.baseURL, c.defaultModel)
	}
}

func TestNewClientRejectsABadBaseURL(t *testing.T) {
	clearEnv(t)
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "unparseable", baseURL: "://nope"},
		{name: "no scheme", baseURL: "api.typesafe.ai"},
		{name: "no host", baseURL: "https:///v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient("k", WithBaseURL(tt.baseURL)); err == nil {
				t.Fatalf("NewClient with base URL %q should fail", tt.baseURL)
			}
		})
	}
}

func TestNewClientIgnoresANilOption(t *testing.T) {
	clearEnv(t)
	if _, err := NewClient("k", nil); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
}

func TestEvaluateSendsTheDocumentedRequest(t *testing.T) {
	clearEnv(t)
	var (
		gotBody   []byte
		gotHeader http.Header
		gotMethod string
		gotPath   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotHeader = r.Method, r.URL.Path, r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		gotBody = body
		w.Header().Set(requestIDHeader, "req_01a0af5b31087f95826953401b685d91")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	resp, err := c.Evaluate(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != pathSystemOne {
		t.Errorf("sent %s %s, want POST %s", gotMethod, gotPath, pathSystemOne)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHeader.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := gotHeader.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := gotHeader.Get("User-Agent"); got != "typesafe-ai-go-sdk/"+Version {
		t.Errorf("User-Agent = %q", got)
	}
	if !reflect.DeepEqual(jsonValue(t, gotBody), jsonValue(t, fixture(t, "request_noul.json"))) {
		t.Errorf("sent %s, want the documented request", gotBody)
	}

	if resp.Model != "jev-latest" {
		t.Errorf("Model = %q", resp.Model)
	}
	if resp.RequestID != "req_01a0af5b31087f95826953401b685d91" {
		t.Errorf("RequestID = %q", resp.RequestID)
	}
	answer, err := resp.Answers.Noul("is_urgent")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if answer.Noul != 0.92 {
		t.Errorf("Noul = %v, want 0.92", answer.Noul)
	}
	if resp.Usage.InputTokens == nil || *resp.Usage.InputTokens != 312 {
		t.Errorf("InputTokens = %v, want 312", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens == nil || *resp.Usage.OutputTokens != 48 {
		t.Errorf("OutputTokens = %v, want 48", resp.Usage.OutputTokens)
	}
}

func TestEvaluateUsesTheConfiguredDefaultModel(t *testing.T) {
	clearEnv(t)
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		gotModel = body.Model
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL, WithDefaultModel("jev-preview"), WithUserAgent(""), WithHTTPClient(nil), WithLogger(nil))
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gotModel != "jev-preview" {
		t.Errorf("model = %q, want jev-preview", gotModel)
	}
}

func TestEvaluateRejectsABadRequestBeforeSending(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), &Request{State: "hi"})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("Evaluate = %v, want a *ValidationError", err)
	}
	if calls.Load() != 0 {
		t.Errorf("the server saw %d requests, want none", calls.Load())
	}
}

func TestEvaluateReportsAnUnencodableState(t *testing.T) {
	clearEnv(t)
	c, _ := newTestClient(t, "https://example.invalid")
	_, err := c.Evaluate(t.Context(), &Request{
		State:     make(chan int),
		Questions: map[string]Question{"q": Noul{Instructions: "?"}},
	})
	if err == nil || !strings.Contains(err.Error(), "encoding the request") {
		t.Fatalf("Evaluate = %v, want an encoding failure", err)
	}
}

func TestEvaluateReportsAnUndecodableResponse(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if err == nil || !strings.Contains(err.Error(), "decoding the "+pathSystemOne+" response") {
		t.Fatalf("Evaluate = %v, want a decoding failure", err)
	}
}

func TestEvaluateReturnsAnAPIError(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set(requestIDHeader, "req_dead")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(fixture(t, "error_unauthorized.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), sampleRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Evaluate = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.Type != "authentication_error" {
		t.Errorf("Type = %q", apiErr.Type)
	}
	if apiErr.RequestID != "req_dead" {
		t.Errorf("RequestID = %q", apiErr.RequestID)
	}
	if calls.Load() != 1 {
		t.Errorf("a rejected key was sent %d times, want once", calls.Load())
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		// Every attempt has to carry the same body; a reader consumed by the
		// first attempt would arrive empty on the second.
		if !reflect.DeepEqual(jsonValue(t, body), jsonValue(t, fixture(t, "request_noul.json"))) {
			t.Errorf("attempt carried %s", body)
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, waits := newTestClient(t, srv.URL)
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("the server saw %d attempts, want 3", calls.Load())
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if !reflect.DeepEqual(waits.recorded(), want) {
		t.Errorf("waited %v, want %v", waits.recorded(), want)
	}
}

func TestDoReturnsTheLastFailureWhenRetriesRunOut(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"detail":{"error_type":"overloaded","message":"try later"}}`))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), sampleRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Evaluate = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != statusOverloaded || apiErr.Message != "try later" {
		t.Errorf("the error lost the last response: %+v", apiErr)
	}
	if calls.Load() != 3 {
		t.Errorf("the server saw %d attempts, want 3", calls.Load())
	}
}

func TestDoWithRetriesDisabled(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, waits := newTestClient(t, srv.URL, WithRetryPolicy(RetryPolicy{}))
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err == nil {
		t.Fatal("Evaluate should have failed")
	}
	if calls.Load() != 1 {
		t.Errorf("the server saw %d attempts, want 1", calls.Load())
	}
	if len(waits.recorded()) != 0 {
		t.Errorf("waited %v with retries disabled", waits.recorded())
	}
}

func TestDoHonoursRetryAfter(t *testing.T) {
	clearEnv(t)
	tests := []struct {
		name   string
		header map[string]string
		want   time.Duration
	}{
		{
			name:   "retry-after-ms",
			header: map[string]string{"retry-after-ms": "1200"},
			want:   1200 * time.Millisecond,
		},
		{
			name:   "retry-after in seconds",
			header: map[string]string{"Retry-After": "3"},
			want:   3 * time.Second,
		},
		{
			name:   "an http date",
			header: map[string]string{"Retry-After": "Thu, 17 Sep 2026 12:00:07 GMT"},
			want:   7 * time.Second,
		},
		{
			name:   "a wait past the cap falls back to the computed backoff",
			header: map[string]string{"Retry-After": "3600"},
			want:   500 * time.Millisecond,
		},
		{
			name:   "an unparseable wait falls back to the computed backoff",
			header: map[string]string{"Retry-After": "soon"},
			want:   500 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					for name, value := range tt.header {
						w.Header().Set(name, value)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = w.Write(fixture(t, "response_noul.json"))
			}))
			defer srv.Close()

			c, waits := newTestClient(t, srv.URL)
			if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			got := waits.recorded()
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("waited %v, want [%v]", got, tt.want)
			}
		})
	}
}

func TestDoRetriesAConnectionError(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	c, waits := newTestClient(t, closedURL)
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if err == nil {
		t.Fatal("Evaluate against a closed server should fail")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Errorf("error %v does not unwrap to a net.Error", err)
	}
	if len(waits.recorded()) != 2 {
		t.Errorf("waited %v, want two retries", waits.recorded())
	}
}

func TestDoRetriesAnAttemptTimeout(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			stall(r, release)
			return
		}
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()
	defer close(release)

	c, waits := newTestClient(t, srv.URL, WithTimeout(25*time.Millisecond))
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("the server saw %d attempts, want 2", calls.Load())
	}
	if len(waits.recorded()) != 1 {
		t.Errorf("waited %v, want one retry", waits.recorded())
	}
}

func TestDoReportsAnAttemptTimeoutAsADeadline(t *testing.T) {
	clearEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		stall(r, release)
	}))
	defer srv.Close()
	defer close(release)

	c, _ := newTestClient(t, srv.URL,
		WithTimeout(25*time.Millisecond),
		WithRetryPolicy(RetryPolicy{}))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not unwrap to context.DeadlineExceeded", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("error %v does not unwrap to a net.Error that timed out", err)
	}
}

func TestDoWithoutAPerAttemptTimeout(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL, WithTimeout(0))
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
}

func TestDoDoesNotRetryAfterTheCallerGivesUp(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(ctx, sampleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Evaluate = %v, want context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Errorf("the server saw %d attempts, want 1", calls.Load())
	}
}

func TestDoStopsWhenTheContextEndsDuringAnAttempt(t *testing.T) {
	clearEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		cancel()
		stall(r, release)
	}))
	defer srv.Close()
	defer close(release)

	c, waits := newTestClient(t, srv.URL)
	_, err := c.Evaluate(ctx, sampleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Evaluate = %v, want context.Canceled", err)
	}
	if len(waits.recorded()) != 0 {
		t.Errorf("waited %v after the caller gave up", waits.recorded())
	}
}

func TestDoReportsAContextThatEndsWhileWaiting(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, waits := newTestClient(t, srv.URL)
	waits.err = context.DeadlineExceeded
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Evaluate = %v, want context.DeadlineExceeded", err)
	}
}

func TestDoRejectsARequestItCannotBuild(t *testing.T) {
	clearEnv(t)
	c, _ := newTestClient(t, "https://example.invalid")
	_, err := c.do(t.Context(), "BAD METHOD", pathModels, nil)
	if err == nil || !strings.Contains(err.Error(), "building the") {
		t.Fatalf("do = %v, want a request-building failure", err)
	}
}

func TestDoTruncatesAHugeErrorBody(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBody+4096)))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), sampleRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Evaluate = %v, want an *APIError", err)
	}
	if len(apiErr.Body) != maxErrorBody {
		t.Errorf("Body is %d bytes, want it capped at %d", len(apiErr.Body), maxErrorBody)
	}
}

// A truncated body must not leave the client blocked on a response it never
// finishes reading, so a follow-up request on the same client still works.
func TestClientSurvivesATruncatedErrorBody(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBody+4096)))
			return
		}
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err == nil {
		t.Fatal("the first request should have failed")
	}
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("the second request failed: %v", err)
	}
}

// A redirect makes the transport replay the body from GetBody, so the second
// leg has to arrive with the same bytes as the first.
func TestDoReplaysTheBodyAcrossARedirect(t *testing.T) {
	clearEnv(t)
	var redirected []byte
	mux := http.NewServeMux()
	mux.HandleFunc(pathSystemOne, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the redirected body: %v", err)
		}
		redirected = body
		_, _ = w.Write(fixture(t, "response_noul.json"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !reflect.DeepEqual(jsonValue(t, redirected), jsonValue(t, fixture(t, "request_noul.json"))) {
		t.Errorf("the redirected leg carried %s", redirected)
	}
}

func TestDoReportsAResponseItCannotRead(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A body shorter than the length announced leaves the client reading
		// past the end of the connection.
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL, WithRetryPolicy(RetryPolicy{}))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if err == nil || !strings.Contains(err.Error(), "reading the") {
		t.Fatalf("Evaluate = %v, want a response-reading failure", err)
	}
}

func TestClientLogsEveryRetry(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var records strings.Builder
	logger := newTestLogger(&records)
	c, _ := newTestClient(t, srv.URL, WithLogger(logger))
	if _, err := c.Evaluate(t.Context(), sampleRequest()); err == nil {
		t.Fatal("Evaluate should have failed")
	}
	if strings.Count(records.String(), "retrying a failed request") != 2 {
		t.Errorf("logged %q, want one record per retry", records.String())
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "response_noul.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			resp, err := c.Evaluate(t.Context(), sampleRequest())
			if err != nil {
				errs <- err
				return
			}
			if _, err := resp.Answers.Noul("is_urgent"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Evaluate: %v", err)
	}
}
