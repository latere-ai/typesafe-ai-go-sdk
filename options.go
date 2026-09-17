// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Option changes one setting of a Client. Options are applied in the order
// they are passed, after the environment has been read, so an option always
// wins over an environment variable.
type Option func(*Client)

// WithBaseURL sends requests to a different API root, replacing the value of
// TYPESAFE_BASE_URL and the built-in default. A trailing slash is trimmed. An
// empty or whitespace-only value is ignored.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if value := strings.TrimSpace(baseURL); value != "" {
			c.baseURL = value
		}
	}
}

// WithHTTPClient sends requests through the given HTTP client, which is where
// a proxy, a custom transport or a connection pool is configured. A nil client
// is ignored. The client's own Timeout, if it sets one, bounds the whole
// attempt including the response body, alongside WithTimeout.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithDefaultModel sets the model used for a Request that leaves Model empty,
// replacing the value of TYPESAFE_DEFAULT_MODEL and the built-in default. An
// empty or whitespace-only value is ignored.
func WithDefaultModel(model string) Option {
	return func(c *Client) {
		if value := strings.TrimSpace(model); value != "" {
			c.defaultModel = value
		}
	}
}

// WithTimeout bounds one attempt, from sending the request to reading the last
// byte of the response. Each retry gets the full timeout again. A non-positive
// duration removes the per-attempt bound and leaves the caller's context as
// the only deadline.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.timeout = timeout }
}

// WithRetryPolicy replaces the retry policy. The zero RetryPolicy disables
// retries; see RetryPolicy for what its other zero fields mean.
func WithRetryPolicy(policy RetryPolicy) Option {
	return func(c *Client) { c.retry = policy }
}

// WithUserAgent replaces the User-Agent header of every request. An empty or
// whitespace-only value is ignored, which leaves the default of
// "typesafe-ai-go-sdk/<version>".
func WithUserAgent(agent string) Option {
	return func(c *Client) {
		if value := strings.TrimSpace(agent); value != "" {
			c.userAgent = value
		}
	}
}

// WithLogger sends the client's diagnostics to the given logger. The client
// logs at debug level only, one record per retry, naming the attempt, the wait
// and the failure that caused it. A nil logger is ignored; the default
// discards every record.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) {
		if logger != nil {
			c.logger = logger
		}
	}
}
