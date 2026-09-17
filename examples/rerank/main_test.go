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

// relevance is one canned noul per candidate, in shortlist order.
var relevance = []float64{0.31, 0.12, 0.94, 0.22, 0.08, 0.86, 0.11, 0.05}

// scored builds a response carrying one noul answer per candidate.
func scored(nouls []float64) string {
	answers := make([]string, 0, len(nouls))
	for i, c := range shortlist {
		answers = append(answers, fmt.Sprintf(`%q: {"type": "noul", "noul": %v}`, c.id, nouls[i]))
	}
	return fmt.Sprintf(`{"model": "jev-1.13.0", "answers": {%s},
		"usage": {"input_tokens": 1042, "output_tokens": 160}}`, strings.Join(answers, ", "))
}

func TestRunSendsOneQuestionPerCandidate(t *testing.T) {
	client, rec := serve(t, http.StatusOK, scored(relevance))
	if err := run(t.Context(), client, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sent struct {
		State struct {
			Query      string `json:"query"`
			Candidates []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"candidates"`
		} `json:"state"`
		Questions map[string]struct {
			Type string `json:"type"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.get(), &sent); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}

	// One call carries the whole shortlist: one state, eight questions.
	if len(sent.Questions) != len(shortlist) {
		t.Fatalf("sent %d questions, want %d", len(sent.Questions), len(shortlist))
	}
	if len(sent.State.Candidates) != len(shortlist) {
		t.Fatalf("state carries %d candidates, want %d", len(sent.State.Candidates), len(shortlist))
	}
	if sent.State.Query != query {
		t.Errorf("state.query = %q, want %q", sent.State.Query, query)
	}
	for i, c := range shortlist {
		got, ok := sent.Questions[c.id]
		if !ok {
			t.Errorf("question %q was not sent", c.id)
			continue
		}
		if got.Type != "noul" {
			t.Errorf("question %q has type %q, want noul", c.id, got.Type)
		}
		// The question id is the candidate id, which is what pairs the answer
		// back to its passage.
		if sent.State.Candidates[i].ID != c.id {
			t.Errorf("candidate %d is %q, want %q", i, sent.State.Candidates[i].ID, c.id)
		}
	}
}

func TestRunReportsTheNewOrder(t *testing.T) {
	client, _ := serve(t, http.StatusOK, scored(relevance))
	var out bytes.Buffer
	if err := run(t.Context(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := "query: How do I rotate the production database password without downtime?\n" +
		"\n" +
		"rank  was  relevance  id       passage\n" +
		"   1    3       0.94  cand_3   Rotate a credential by adding the new password as a second ...\n" +
		"   2    6       0.86  cand_6   The connection pool reloads its credentials when the mounte...\n" +
		"   3    1       0.31  cand_1   Database passwords are stored in the secret manager under t...\n" +
		"   4    4       0.22  cand_4   The password policy requires 24 characters, rotation every ...\n" +
		"   5    2       0.12  cand_2   To restart the production database, drain the connection po...\n" +
		"   6    7       0.11  cand_7   Production access requires a break-glass ticket. The ticket...\n" +
		"   7    5       0.08  cand_5   Downtime during a deploy usually comes from a rollout that ...\n" +
		"   8    8       0.05  cand_8   Staging databases are reset nightly, so a password set ther...\n" +
		"\n" +
		"top hit moved up from position 3\n"
	if got := out.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
}

// TestRunKeepsTheIndexOrderOnATie pins the stable sort: two passages the model
// rates the same keep the order the keyword index gave them.
func TestRunKeepsTheIndexOrderOnATie(t *testing.T) {
	tied := make([]float64, len(shortlist))
	for i := range tied {
		tied[i] = 0.5
	}
	client, _ := serve(t, http.StatusOK, scored(tied))
	var out bytes.Buffer
	if err := run(t.Context(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for i, c := range shortlist {
		line := lines[3+i] // two header lines, one blank, then the table
		if !strings.Contains(line, c.id) {
			t.Errorf("row %d is %q, want candidate %q", i+1, line, c.id)
		}
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet("short", 10); got != "short" {
		t.Errorf("a passage under the width came back as %q", got)
	}
	if got := snippet("0123456789ab", 10); got != "0123456..." {
		t.Errorf("a passage over the width came back as %q", got)
	}
}

func TestRunReportsAPIError(t *testing.T) {
	// 400 is not retryable, so the call returns on the first attempt.
	client, _ := serve(t, http.StatusBadRequest, `{"detail": {
		"error_type": "invalid_request_error",
		"message": "state must not be empty"
	}}`)

	err := run(t.Context(), client, &bytes.Buffer{})
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("run returned %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", apiErr.StatusCode)
	}
}

func TestRunReportsATransportFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, scored(relevance))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := run(ctx, client, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want a wrapped context.Canceled", err)
	}
}

func TestRunReportsAMissingCandidate(t *testing.T) {
	client, _ := serve(t, http.StatusOK,
		`{"model": "jev-1.13.0", "answers": {"cand_1": {"type": "noul", "noul": 0.3}}, "usage": {}}`)
	if err := run(t.Context(), client, &bytes.Buffer{}); err == nil {
		t.Fatal("run accepted a response missing seven of the eight answers")
	}
}

// failingWriter reports a write failure, which is what the single write at the
// end of run has to return rather than swallow.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk is full") }

func TestRunReportsAWriteFailure(t *testing.T) {
	client, _ := serve(t, http.StatusOK, scored(relevance))
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
		_, _ = w.Write([]byte(scored(relevance)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.EnvAPIKey, "test-key")
	t.Setenv(typesafe.EnvBaseURL, srv.URL)
	main()
}
