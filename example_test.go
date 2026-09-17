// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

// serve starts a server that answers every request with one canned body, and
// returns a client pointed at it. A real program passes no base URL and lets
// the client reach https://api.typesafe.ai.
func serve(status int, body string) (*typesafe.Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-typesafe-request-id", "req_01a0af5b31087f95826953401b685d91")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	client, err := typesafe.NewClient("demo-key", typesafe.WithBaseURL(srv.URL))
	if err != nil {
		panic(err)
	}
	return client, srv.Close
}

// Evaluate one piece of text against a yes/no question.
func Example() {
	client, stop := serve(http.StatusOK, `{
		"model": "jev-1.13.0",
		"answers": {"is_urgent": {"type": "noul", "noul": 0.92}},
		"usage": {"input_tokens": 312, "output_tokens": 48}
	}`)
	defer stop()

	resp, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
		},
	})
	if err != nil {
		fmt.Println("evaluate:", err)
		return
	}

	urgent, err := resp.Answers.Noul("is_urgent")
	if err != nil {
		fmt.Println("answer:", err)
		return
	}
	fmt.Printf("model %s answered %.2f\n", resp.Model, urgent.Noul)
	// Output:
	// model jev-1.13.0 answered 0.92
}

// One request can carry all three question types. Every question sees the same
// state and is answered on its own.
func ExampleClient_Evaluate() {
	client, stop := serve(http.StatusOK, `{
		"model": "jev-1.13.0",
		"answers": {
			"is_urgent": {"type": "noul", "noul": 0.95},
			"department": {
				"type": "choice",
				"choice": "billing",
				"probabilities": {"billing": 0.96, "sales": 0.0, "technical": 0.04},
				"confidence": 0.94
			},
			"frustration": {
				"type": "score",
				"score": 1.03,
				"legend": {"0": "Calm", "1": "Frustrated", "2": "Very angry"},
				"probabilities": {"0": 0.0, "1": 0.97, "2": 0.03},
				"confidence": 0.95
			}
		},
		"usage": {"input_tokens": 378, "output_tokens": 73}
	}`)
	defer stop()

	resp, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True:  "Explicitly time-sensitive",
					False: "No urgency expressed",
				},
			},
			"department": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: map[string]any{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts",
				},
			},
			"frustration": typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     []any{"Calm", "Frustrated", "Very angry"},
			},
		},
	})
	if err != nil {
		fmt.Println("evaluate:", err)
		return
	}

	urgent, err := resp.Answers.Noul("is_urgent")
	if err != nil {
		fmt.Println("answer:", err)
		return
	}
	department, err := resp.Answers.Choice("department")
	if err != nil {
		fmt.Println("answer:", err)
		return
	}
	frustration, err := resp.Answers.Score("frustration")
	if err != nil {
		fmt.Println("answer:", err)
		return
	}

	fmt.Printf("urgent:      %.2f\n", urgent.Noul)
	fmt.Printf("department:  %s (%.0f%% confident)\n", department.Choice, department.Confidence*100)
	fmt.Printf("frustration: %.2f of %d levels\n", frustration.Score, len(frustration.Legend))
	// Output:
	// urgent:      0.95
	// department:  billing (94% confident)
	// frustration: 1.03 of 3 levels
}

// A score answer's legend maps level indices back to the rubric that was sent.
// Levels puts them back in rubric order.
func ExampleScoreAnswer_Levels() {
	client, stop := serve(http.StatusOK, `{
		"model": "jev-1.13.0",
		"answers": {
			"frustration": {
				"type": "score",
				"score": 1.6,
				"legend": {"0": "Calm", "1": "Frustrated", "2": "Very angry"},
				"probabilities": {"0": 0.05, "1": 0.3, "2": 0.65},
				"confidence": 0.78
			}
		},
		"usage": {"input_tokens": 312, "output_tokens": 48}
	}`)
	defer stop()

	resp, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"frustration": typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     []any{"Calm", "Frustrated", "Very angry"},
			},
		},
	})
	if err != nil {
		fmt.Println("evaluate:", err)
		return
	}
	frustration, err := resp.Answers.Score("frustration")
	if err != nil {
		fmt.Println("answer:", err)
		return
	}
	levels, err := frustration.Levels()
	if err != nil {
		fmt.Println("levels:", err)
		return
	}
	for i, level := range levels {
		fmt.Printf("%d: %v (p=%.2f)\n", i, level, frustration.Probabilities[fmt.Sprint(i)])
	}
	// Output:
	// 0: Calm (p=0.05)
	// 1: Frustrated (p=0.30)
	// 2: Very angry (p=0.65)
}

// A failure the API reported comes back as an *APIError carrying the status,
// the server's error type, the message and the request id.
func ExampleAPIError() {
	client, stop := serve(http.StatusUnauthorized, `{
		"detail": {
			"error_type": "authentication_error",
			"message": "Cannot authenticate with the server. Please check your API key and try again."
		}
	}`)
	defer stop()

	_, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
		},
	})

	var apiErr *typesafe.APIError
	switch {
	case errors.As(err, &apiErr):
		fmt.Printf("status %d, type %s, retryable %v\n", apiErr.StatusCode, apiErr.Type, apiErr.Temporary())
		fmt.Println("request id:", apiErr.RequestID)
	case err != nil:
		fmt.Println("evaluate:", err)
	}
	// Output:
	// status 401, type authentication_error, retryable false
	// request id: req_01a0af5b31087f95826953401b685d91
}

// A request the API would reject on its shape is rejected before it is sent,
// with the offending field named.
func ExampleValidationError() {
	client, stop := serve(http.StatusOK, `{}`)
	defer stop()

	_, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"frustration": typesafe.Score{Criteria: []any{"Calm"}},
		},
	})

	var invalid *typesafe.ValidationError
	if errors.As(err, &invalid) {
		fmt.Println(invalid.Field, "-", invalid.Message)
	}
	// Output:
	// questions[frustration].Criteria - must hold at least two levels
}

// ListModels reports the models and aliases the account may name in
// Request.Model.
func ExampleClient_ListModels() {
	client, stop := serve(http.StatusOK, `{"models": [
		{"name": "jev-latest", "description": "Alias for the newest release.", "release_date": "2026-09-10T18:38:01.391457+00:00"},
		{"name": "jev-preview", "description": "Alias for the next release.", "release_date": "2026-09-10T18:38:01.391457+00:00"}
	]}`)
	defer stop()

	models, err := client.ListModels(context.Background())
	if err != nil {
		fmt.Println("list models:", err)
		return
	}
	for _, model := range models {
		fmt.Printf("%s released %s\n", model.Name, model.ReleaseDate.UTC().Format(time.DateOnly))
	}
	// Output:
	// jev-latest released 2026-09-10
	// jev-preview released 2026-09-10
}

// The retry policy, the per-attempt timeout and the default model are set on
// the client and apply to every request it sends.
func ExampleNewClient_options() {
	client, err := typesafe.NewClient("demo-key",
		typesafe.WithBaseURL("https://api.typesafe.ai"),
		typesafe.WithDefaultModel("jev-1.13.0"),
		typesafe.WithTimeout(30*time.Second),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{
			MaxRetries:     4,
			InitialBackoff: time.Second,
			MaxBackoff:     20 * time.Second,
			Jitter:         0.3,
			MaxRetryAfter:  30 * time.Second,
		}),
		typesafe.WithUserAgent("support-triage/1.4"),
	)
	if err != nil {
		fmt.Println("new client:", err)
		return
	}
	fmt.Println(client != nil)
	// Output:
	// true
}
