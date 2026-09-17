// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

// newTestLogger writes records to w at debug level, so a test can read what
// the client logged.
func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestOptionsOverrideEverything(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvBaseURL, "https://from-env.example.com")
	t.Setenv(EnvDefaultModel, "from-env")

	httpClient := &http.Client{Transport: http.DefaultTransport}
	logger := newTestLogger(io.Discard)
	policy := RetryPolicy{MaxRetries: 9, InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: 0.5, MaxRetryAfter: time.Hour}

	c, err := NewClient("k",
		WithBaseURL(" https://from-option.example.com/ "),
		WithDefaultModel(" jev-1.13.0 "),
		WithHTTPClient(httpClient),
		WithLogger(logger),
		WithRetryPolicy(policy),
		WithTimeout(3*time.Second),
		WithUserAgent(" my-app/1.0 "),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != "https://from-option.example.com" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.defaultModel != "jev-1.13.0" {
		t.Errorf("defaultModel = %q", c.defaultModel)
	}
	if c.httpClient != httpClient {
		t.Error("WithHTTPClient did not take")
	}
	if c.logger != logger {
		t.Error("WithLogger did not take")
	}
	if c.retry != policy {
		t.Errorf("retry = %+v, want %+v", c.retry, policy)
	}
	if c.timeout != 3*time.Second {
		t.Errorf("timeout = %v", c.timeout)
	}
	if c.userAgent != "my-app/1.0" {
		t.Errorf("userAgent = %q", c.userAgent)
	}
}

func TestOptionsIgnoreEmptyValues(t *testing.T) {
	clearEnv(t)
	c, err := NewClient("k",
		WithBaseURL("   "),
		WithDefaultModel("\t"),
		WithUserAgent(""),
		WithHTTPClient(nil),
		WithLogger(nil),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want the default", c.baseURL)
	}
	if c.defaultModel != DefaultModel {
		t.Errorf("defaultModel = %q, want the default", c.defaultModel)
	}
	if c.userAgent != defaultUserAgent {
		t.Errorf("userAgent = %q, want the default", c.userAgent)
	}
	if c.httpClient == nil || c.logger == nil {
		t.Error("a nil HTTP client or logger replaced the default")
	}
}

func TestWithTimeoutAcceptsANonPositiveDuration(t *testing.T) {
	clearEnv(t)
	c, err := NewClient("k", WithTimeout(-1))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.timeout != -1 {
		t.Errorf("timeout = %v, want the value as passed", c.timeout)
	}
}

func TestVersionIsSet(t *testing.T) {
	if Version == "" {
		t.Error("Version is empty")
	}
	if defaultUserAgent != "typesafe-ai-go-sdk/"+Version {
		t.Errorf("defaultUserAgent = %q", defaultUserAgent)
	}
}
