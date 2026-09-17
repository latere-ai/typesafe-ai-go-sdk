// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

// recorder keeps the request body the program sent. The handler runs on the
// server's goroutine, so the lock is what makes the body safe to read back.
type recorder struct {
	mu   sync.Mutex
	body []byte
}

func (r *recorder) set(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.body = body
}

func (r *recorder) get() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body
}

// serve starts a server answering every request with one canned body, and
// returns a client pointed at it together with the recorded request.
func serve(t *testing.T, status int, body string) (*typesafe.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		rec.set(sent)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-typesafe-request-id", "req_01a0af5b31087f95826953401b685d91")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := typesafe.NewClient("test-key", typesafe.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, rec
}

// canned is one full set of answers. base fills every field with a realistic
// value, and a case overrides only what it is about.
type canned struct {
	function      string
	functionAt    float64
	service       string
	serviceAt     float64
	environment   string
	environmentAt float64
	logLevel      string
	logLevelAt    float64
	mentions      float64
}

func base() canned {
	return canned{
		function: "rollback_deployment", functionAt: 0.93,
		service: "checkout", serviceAt: 0.97,
		environment: "production", environmentAt: 0.99,
		logLevel: "error", logLevelAt: 0.88,
		mentions: 0.96,
	}
}

// choiceAnswer renders one choice answer over the options its question holds.
func choiceAnswer(choice string, confidence float64, options ...string) string {
	probs := make([]string, 0, len(options))
	for _, o := range options {
		p := 0.02
		if o == choice {
			p = 0.94
		}
		probs = append(probs, fmt.Sprintf("%q: %v", o, p))
	}
	return fmt.Sprintf(`{"type": "choice", "choice": %q, "probabilities": {%s}, "confidence": %v}`,
		choice, strings.Join(probs, ", "), confidence)
}

func (c canned) body() string {
	return fmt.Sprintf(`{"model": "jev-1.13.0", "answers": {
		"function": %s,
		"service": %s,
		"environment": %s,
		"log_level": %s,
		"mentions_service": {"type": "noul", "noul": %v}
	}, "usage": {"input_tokens": 296, "output_tokens": 84}}`,
		choiceAnswer(c.function, c.functionAt,
			"list_deployments", "rollback_deployment", "tail_logs", "open_incident"),
		choiceAnswer(c.service, c.serviceAt, "payments", "checkout", "search"),
		choiceAnswer(c.environment, c.environmentAt, "production", "staging"),
		choiceAnswer(c.logLevel, c.logLevelAt, "error", "warn", "info"),
		c.mentions)
}

func TestRunSendsTheFunctionAndEveryArgument(t *testing.T) {
	client, rec := serve(t, http.StatusOK, base().body())
	if err := run(t.Context(), client, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sent struct {
		State     string `json:"state"`
		Questions map[string]struct {
			Type     string         `json:"type"`
			Criteria map[string]any `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.get(), &sent); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}
	if sent.State != request {
		t.Errorf("state = %q, want %q", sent.State, request)
	}
	want := map[string]string{
		"function":         "choice",
		"service":          "choice",
		"environment":      "choice",
		"log_level":        "choice",
		"mentions_service": "noul",
	}
	if len(sent.Questions) != len(want) {
		t.Fatalf("sent %d questions, want %d", len(sent.Questions), len(want))
	}
	for id, kind := range want {
		got, ok := sent.Questions[id]
		if !ok {
			t.Errorf("question %q was not sent", id)
			continue
		}
		if got.Type != kind {
			t.Errorf("question %q has type %q, want %q", id, got.Type, kind)
		}
	}
	// The function question offers exactly the dispatch table's keys, which is
	// what lets the chosen label index it without a mapping step.
	options := sent.Questions["function"].Criteria
	if len(options) != len(tools) {
		t.Fatalf("the function question offers %d options, want %d", len(options), len(tools))
	}
	for name := range tools {
		if _, ok := options[name]; !ok {
			t.Errorf("the function question does not offer %q", name)
		}
	}
}

func TestRunDispatches(t *testing.T) {
	const header = "request:  roll back checkout in production, the last deploy broke the cart\n"

	cases := []struct {
		name string
		body func() canned
		want string
	}{{
		name: "rollback fills two arguments",
		body: base,
		want: header +
			"function: rollback_deployment (confidence 0.93)\n" +
			"argument: service     = checkout   (confidence 0.97)\n" +
			"argument: environment = production (confidence 0.99)\n" +
			"result:   rolled checkout in production back to its previous revision\n",
	}, {
		name: "list deployments takes no service",
		body: func() canned {
			c := base()
			c.function, c.functionAt = "list_deployments", 0.88
			return c
		},
		want: header +
			"function: list_deployments (confidence 0.88)\n" +
			"argument: environment = production (confidence 0.99)\n" +
			"result:   listed the running deployments in production\n",
	}, {
		name: "tail logs fills three arguments",
		body: func() canned {
			c := base()
			c.function, c.functionAt = "tail_logs", 0.91
			return c
		},
		want: header +
			"function: tail_logs (confidence 0.91)\n" +
			"argument: service     = checkout   (confidence 0.97)\n" +
			"argument: environment = production (confidence 0.99)\n" +
			"argument: log_level   = error      (confidence 0.88)\n" +
			"result:   following error logs for checkout in production\n",
	}, {
		name: "open incident takes only a service",
		body: func() canned {
			c := base()
			c.function, c.functionAt = "open_incident", 0.79
			return c
		},
		want: header +
			"function: open_incident (confidence 0.79)\n" +
			"argument: service     = checkout   (confidence 0.97)\n" +
			"result:   opened an incident for checkout and paged the on-call engineer\n",
	}, {
		// Below the function bar nothing downstream is read, so no argument
		// line appears at all.
		name: "an unclear function refuses",
		body: func() canned {
			c := base()
			c.functionAt = 0.44
			return c
		},
		want: header +
			"function: rollback_deployment (confidence 0.44)\n" +
			"refused:  which function to call is not clear enough (confidence below 0.70)\n",
	}, {
		// The argument that failed is still reported, so the refusal names
		// what would have to be clarified.
		name: "an unclear argument refuses",
		body: func() canned {
			c := base()
			c.environmentAt = 0.51
			return c
		},
		want: header +
			"function: rollback_deployment (confidence 0.93)\n" +
			"argument: service     = checkout   (confidence 0.97)\n" +
			"argument: environment = production (confidence 0.51)\n" +
			"refused:  the environment argument is not clear enough (confidence below 0.60)\n",
	}, {
		// A choice always returns its most probable option. Without the noul,
		// a request that named no service would still get one.
		name: "a request that names no service refuses",
		body: func() canned {
			c := base()
			c.mentions = 0.09
			return c
		},
		want: header +
			"function: rollback_deployment (confidence 0.93)\n" +
			"refused:  the request does not name a service (p=0.09)\n",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := serve(t, http.StatusOK, c.body().body())
			var out bytes.Buffer
			if err := run(t.Context(), client, &out); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := out.String(); got != c.want {
				t.Errorf("output:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

func TestRunRejectsAFunctionOutsideTheTable(t *testing.T) {
	c := base()
	c.function = "drop_database"
	client, _ := serve(t, http.StatusOK, c.body())

	err := run(t.Context(), client, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "drop_database") {
		t.Fatalf("run returned %v, want a failure naming the unknown function", err)
	}
}

func TestRunReportsAPIError(t *testing.T) {
	// 403 is not retryable, so the call returns on the first attempt.
	client, _ := serve(t, http.StatusForbidden, `{"detail": {
		"error_type": "permission_error",
		"message": "This key may not use that model."
	}}`)

	err := run(t.Context(), client, &bytes.Buffer{})
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("run returned %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.Type != "permission_error" {
		t.Errorf("status %d type %q, want 403 permission_error", apiErr.StatusCode, apiErr.Type)
	}
}

func TestRunReportsATransportFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, base().body())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := run(ctx, client, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want a wrapped context.Canceled", err)
	}
}

func TestRunReportsAMissingAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{{
		name: "no function",
		body: `{"model": "jev-1.13.0", "answers": {}, "usage": {}}`,
	}, {
		name: "no mentions_service",
		body: fmt.Sprintf(`{"model": "jev-1.13.0", "answers": {"function": %s}, "usage": {}}`,
			choiceAnswer("rollback_deployment", 0.93, "rollback_deployment")),
	}, {
		name: "no environment",
		body: fmt.Sprintf(`{"model": "jev-1.13.0", "answers": {
			"function": %s, "service": %s,
			"mentions_service": {"type": "noul", "noul": 0.96}}, "usage": {}}`,
			choiceAnswer("rollback_deployment", 0.93, "rollback_deployment"),
			choiceAnswer("checkout", 0.97, "checkout")),
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := serve(t, http.StatusOK, c.body)
			if err := run(t.Context(), client, &bytes.Buffer{}); err == nil {
				t.Fatal("run accepted a response with a missing answer")
			}
		})
	}
}

// failingWriter reports a write failure, which is what the single write at the
// end of run has to return rather than swallow.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk is full") }

func TestRunReportsAWriteFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, base().body())
	if err := run(t.Context(), client, failingWriter{}); err == nil {
		t.Fatal("run swallowed a write failure")
	}
}

// TestSetIgnoresAnUnknownArgument pins that an id no field matches leaves the
// arguments untouched rather than landing in the wrong one.
func TestSetIgnoresAnUnknownArgument(t *testing.T) {
	var args arguments
	args.set("region", "eu-central-1")
	if args != (arguments{}) {
		t.Fatalf("set stored an unknown argument: %+v", args)
	}
}

func TestEvaluateNeedsAnAPIKey(t *testing.T) {
	t.Setenv(typesafe.EnvAPIKey, "")
	if err := evaluate(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("evaluate built a client without an API key")
	}
}

// TestMainRuns drives main itself against the test server, which is the only
// way the program's entry point is exercised. The success path returns rather
// than exiting, so the test survives it.
func TestMainRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(base().body()))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.EnvAPIKey, "test-key")
	t.Setenv(typesafe.EnvBaseURL, srv.URL)
	main()
}
