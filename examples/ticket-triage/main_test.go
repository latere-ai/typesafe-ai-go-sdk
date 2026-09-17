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

// answered builds a response body carrying the four answers the program sends
// questions for.
func answered(category string, confidence, refund, severity, frustration float64) string {
	return fmt.Sprintf(`{
		"model": "jev-1.13.0",
		"answers": {
			"category": {
				"type": "choice",
				"choice": %q,
				"probabilities": {"account": 0.03, "billing": 0.9, "bug_report": 0.05, "feature_request": 0.02},
				"confidence": %v
			},
			"refund_requested": {"type": "noul", "noul": %v},
			"severity": {
				"type": "score",
				"score": %v,
				"legend": {"0": "Cosmetic", "1": "Degraded", "2": "Blocking"},
				"probabilities": {"0": 0.1, "1": 0.7, "2": 0.2},
				"confidence": 0.81
			},
			"frustration": {
				"type": "score",
				"score": %v,
				"legend": {"0": "Calm", "1": "Frustrated", "2": "Angry"},
				"probabilities": {"0": 0.05, "1": 0.35, "2": 0.6},
				"confidence": 0.79
			}
		},
		"usage": {"input_tokens": 412, "output_tokens": 96}
	}`, category, confidence, refund, severity, frustration)
}

func TestRunSendsEveryQuestionInOneRequest(t *testing.T) {
	client, rec := serve(t, http.StatusOK, answered("billing", 0.94, 0.88, 1.2, 1.7))
	if err := run(t.Context(), client, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sent struct {
		State     map[string]any `json:"state"`
		Model     string         `json:"model"`
		Questions map[string]struct {
			Type string `json:"type"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.get(), &sent); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}
	want := map[string]string{
		"category":         "choice",
		"refund_requested": "noul",
		"severity":         "score",
		"frustration":      "score",
	}
	if len(sent.Questions) != len(want) {
		t.Fatalf("sent %d questions, want %d: %v", len(sent.Questions), len(want), sent.Questions)
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
	// The state is sent as an object so each part keeps its own name.
	if sent.State["customer_plan"] != "business" {
		t.Errorf("state.customer_plan = %v, want business", sent.State["customer_plan"])
	}
	if sent.Model == "" {
		t.Error("the request carries no model")
	}
}

func TestRunRoutes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{{
		name: "billing with a refund asked for",
		body: answered("billing", 0.94, 0.88, 1.2, 1.7),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    billing (confidence 0.94)\n" +
			"frustration: 1.70 of 2\n" +
			"decision:    billing queue, refund flagged (p=0.88)\n" +
			"priority:    same-day response, the customer is angry\n",
	}, {
		name: "billing with no refund asked for",
		body: answered("billing", 0.91, 0.12, 1.2, 0.4),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    billing (confidence 0.91)\n" +
			"frustration: 0.40 of 2\n" +
			"decision:    billing queue\n",
	}, {
		name: "blocking bug report",
		body: answered("bug_report", 0.88, 0.05, 1.9, 1.1),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    bug_report (confidence 0.88)\n" +
			"frustration: 1.10 of 2\n" +
			"decision:    engineering, severity 1.90 of 2\n",
	}, {
		name: "bug report with a workaround",
		body: answered("bug_report", 0.86, 0.05, 0.9, 1.1),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    bug_report (confidence 0.86)\n" +
			"frustration: 1.10 of 2\n" +
			"decision:    bug backlog, severity 0.90 of 2\n",
	}, {
		name: "account",
		body: answered("account", 0.97, 0.02, 0.3, 0.2),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    account (confidence 0.97)\n" +
			"frustration: 0.20 of 2\n" +
			"decision:    account support queue\n",
	}, {
		name: "feature request",
		body: answered("feature_request", 0.83, 0.01, 0.2, 0.1),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    feature_request (confidence 0.83)\n" +
			"frustration: 0.10 of 2\n" +
			"decision:    product backlog\n",
	}, {
		// Below the threshold the branch is not taken at all, so no
		// speculative answer reaches the decision.
		name: "low confidence goes to a human",
		body: answered("billing", 0.41, 0.88, 1.9, 0.8),
		want: "ticket:      Charged twice for order #98423, and now I cannot log in\n" +
			"category:    billing (confidence 0.41)\n" +
			"frustration: 0.80 of 2\n" +
			"decision:    human review (confidence below 0.75)\n",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := serve(t, http.StatusOK, c.body)
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

func TestRunReportsAPIError(t *testing.T) {
	// 401 is not retryable, so the call returns on the first attempt.
	client, _ := serve(t, http.StatusUnauthorized, `{"detail": {
		"error_type": "authentication_error",
		"message": "Cannot authenticate with the server."
	}}`)

	err := run(t.Context(), client, &bytes.Buffer{})
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("run returned %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Type != "authentication_error" {
		t.Errorf("status %d type %q, want 401 authentication_error", apiErr.StatusCode, apiErr.Type)
	}
	if apiErr.RequestID != "req_01a0af5b31087f95826953401b685d91" {
		t.Errorf("request id %q", apiErr.RequestID)
	}
}

func TestRunReportsATransportFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, answered("billing", 0.94, 0.88, 1.2, 1.7))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := run(ctx, client, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want a wrapped context.Canceled", err)
	}
}

func TestRunReportsAMissingAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{{
		name: "no category",
		body: `{"model": "jev-1.13.0", "answers": {}, "usage": {}}`,
	}, {
		name: "no frustration",
		body: `{"model": "jev-1.13.0", "answers": {"category": {
			"type": "choice", "choice": "billing", "probabilities": {"billing": 1},
			"confidence": 0.94}}, "usage": {}}`,
	}, {
		name: "no refund on the billing branch",
		body: `{"model": "jev-1.13.0", "answers": {
			"category": {"type": "choice", "choice": "billing",
				"probabilities": {"billing": 1}, "confidence": 0.94},
			"frustration": {"type": "score", "score": 1.1,
				"legend": {"0": "Calm", "1": "Angry"},
				"probabilities": {"0": 0.4, "1": 0.6}, "confidence": 0.7}
		}, "usage": {}}`,
	}, {
		name: "no severity on the bug branch",
		body: `{"model": "jev-1.13.0", "answers": {
			"category": {"type": "choice", "choice": "bug_report",
				"probabilities": {"bug_report": 1}, "confidence": 0.94},
			"frustration": {"type": "score", "score": 1.1,
				"legend": {"0": "Calm", "1": "Angry"},
				"probabilities": {"0": 0.4, "1": 0.6}, "confidence": 0.7}
		}, "usage": {}}`,
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
	client, _ := serve(t, http.StatusOK, answered("account", 0.97, 0.02, 0.3, 0.2))
	if err := run(t.Context(), client, failingWriter{}); err == nil {
		t.Fatal("run swallowed a write failure")
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
		_, _ = w.Write([]byte(answered("billing", 0.94, 0.88, 1.2, 1.7)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.EnvAPIKey, "test-key")
	t.Setenv(typesafe.EnvBaseURL, srv.URL)
	main()
}
