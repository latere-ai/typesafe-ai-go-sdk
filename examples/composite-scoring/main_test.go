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

// scored builds a response carrying one score answer per dimension, in the
// order this file's dimensions are declared.
func scored(raw ...float64) string {
	if len(raw) != len(dimensions) {
		panic("scored needs one value per dimension")
	}
	answers := make([]string, 0, len(raw))
	for i, d := range dimensions {
		legend := make([]string, 0, len(d.rubric))
		probs := make([]string, 0, len(d.rubric))
		for level := range d.rubric {
			legend = append(legend, fmt.Sprintf(`"%d": "level %d"`, level, level))
			probs = append(probs, fmt.Sprintf(`"%d": %v`, level, 1.0/float64(len(d.rubric))))
		}
		answers = append(answers, fmt.Sprintf(
			`%q: {"type": "score", "score": %v, "legend": {%s}, "probabilities": {%s}, "confidence": 0.82}`,
			d.id, raw[i], strings.Join(legend, ", "), strings.Join(probs, ", ")))
	}
	return fmt.Sprintf(`{"model": "jev-1.13.0", "answers": {%s},
		"usage": {"input_tokens": 486, "output_tokens": 112}}`, strings.Join(answers, ", "))
}

func TestRunSendsOneScoreQuestionPerDimension(t *testing.T) {
	client, rec := serve(t, http.StatusOK, scored(3.1, 2.4, 3.6, 0.8))
	if err := run(t.Context(), client, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sent struct {
		Questions map[string]struct {
			Type     string `json:"type"`
			Criteria []any  `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.get(), &sent); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}
	if len(sent.Questions) != len(dimensions) {
		t.Fatalf("sent %d questions, want %d", len(sent.Questions), len(dimensions))
	}
	for _, d := range dimensions {
		got, ok := sent.Questions[d.id]
		if !ok {
			t.Errorf("question %q was not sent", d.id)
			continue
		}
		if got.Type != "score" {
			t.Errorf("question %q has type %q, want score", d.id, got.Type)
		}
		// The rubric the program normalises by is the rubric it sent.
		if len(got.Criteria) != len(d.rubric) {
			t.Errorf("question %q sent %d levels, want %d", d.id, len(got.Criteria), len(d.rubric))
		}
	}
}

// TestWeightsSumToOne pins the property that makes the composite readable on
// the same 0 to 1 scale as each normalised dimension.
func TestWeightsSumToOne(t *testing.T) {
	total := 0.0
	for _, d := range dimensions {
		total += d.weight
	}
	if total < 0.999 || total > 1.001 {
		t.Fatalf("the weights sum to %v, want 1", total)
	}
}

func TestRunReportsTheBreakdown(t *testing.T) {
	cases := []struct {
		name string
		raw  []float64
		want string
	}{{
		// 0.30*0.775 + 0.25*0.800 + 0.30*0.900 + 0.15*0.400 = 0.7025.
		name: "a description that needs one more detail",
		raw:  []float64{3.1, 2.4, 3.6, 0.8},
		want: "pull request: Add a retry budget to the payments client\n" +
			"\n" +
			"dimension             raw    of  normalised  weight  contribution\n" +
			"problem stated       3.10     4       0.775    0.30         0.232\n" +
			"change described     2.40     3       0.800    0.25         0.200\n" +
			"test evidence        3.60     4       0.900    0.30         0.270\n" +
			"risk and rollback    0.80     2       0.400    0.15         0.060\n" +
			"\n" +
			"weighted total: 0.763 of 1.000\n" +
			"verdict:        ready for review\n",
	}, {
		// Every dimension at its top level puts the composite at 1.
		name: "a complete description",
		raw:  []float64{4, 3, 4, 2},
		want: "pull request: Add a retry budget to the payments client\n" +
			"\n" +
			"dimension             raw    of  normalised  weight  contribution\n" +
			"problem stated       4.00     4       1.000    0.30         0.300\n" +
			"change described     3.00     3       1.000    0.25         0.250\n" +
			"test evidence        4.00     4       1.000    0.30         0.300\n" +
			"risk and rollback    2.00     2       1.000    0.15         0.150\n" +
			"\n" +
			"weighted total: 1.000 of 1.000\n" +
			"verdict:        ready for review\n",
	}, {
		name: "a thin description",
		raw:  []float64{1.2, 1.0, 0.4, 0.2},
		want: "pull request: Add a retry budget to the payments client\n" +
			"\n" +
			"dimension             raw    of  normalised  weight  contribution\n" +
			"problem stated       1.20     4       0.300    0.30         0.090\n" +
			"change described     1.00     3       0.333    0.25         0.083\n" +
			"test evidence        0.40     4       0.100    0.30         0.030\n" +
			"risk and rollback    0.20     2       0.100    0.15         0.015\n" +
			"\n" +
			"weighted total: 0.218 of 1.000\n" +
			"verdict:        send back, the description does not describe the change\n",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := serve(t, http.StatusOK, scored(c.raw...))
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

func TestVerdictFor(t *testing.T) {
	cases := []struct {
		total float64
		want  string
	}{
		{0.95, "ready for review"},
		{readyToReview, "ready for review"},
		{0.60, "ask the author for the missing detail"},
		{needsDetail, "ask the author for the missing detail"},
		{0.10, "send back, the description does not describe the change"},
	}
	for _, c := range cases {
		if got := verdictFor(c.total); got != c.want {
			t.Errorf("verdictFor(%v) = %q, want %q", c.total, got, c.want)
		}
	}
}

func TestRunReportsAPIError(t *testing.T) {
	// 422 is not retryable, so the call returns on the first attempt.
	client, _ := serve(t, http.StatusUnprocessableEntity, `{"detail": {
		"error_type": "invalid_request_error",
		"message": "criteria must hold at least two levels"
	}}`)

	err := run(t.Context(), client, &bytes.Buffer{})
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("run returned %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", apiErr.StatusCode)
	}
}

func TestRunReportsATransportFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, scored(3.1, 2.4, 3.6, 0.8))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := run(ctx, client, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want a wrapped context.Canceled", err)
	}
}

func TestRunReportsAMissingDimension(t *testing.T) {
	client, _ := serve(t, http.StatusOK, `{"model": "jev-1.13.0", "answers": {}, "usage": {}}`)
	if err := run(t.Context(), client, &bytes.Buffer{}); err == nil {
		t.Fatal("run accepted a response with no answers")
	}
}

// failingWriter reports a write failure, which is what the single write at the
// end of run has to return rather than swallow.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk is full") }

func TestRunReportsAWriteFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, scored(3.1, 2.4, 3.6, 0.8))
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
		_, _ = w.Write([]byte(scored(3.1, 2.4, 3.6, 0.8)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.EnvAPIKey, "test-key")
	t.Setenv(typesafe.EnvBaseURL, srv.URL)
	main()
}
