// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

// TestLiveEvaluate runs one request of each question type, and a model
// listing, against the real API. It runs only when TYPESAFE_API_KEY names a
// key, because it spends one; every other run skips.
func TestLiveEvaluate(t *testing.T) {
	if strings.TrimSpace(os.Getenv(typesafe.EnvAPIKey)) == "" {
		t.Skip("set " + typesafe.EnvAPIKey + " to run against the live API")
	}

	client, err := typesafe.NewClient("")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	resp, err := client.Evaluate(ctx, &typesafe.Request{
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
		t.Fatalf("Evaluate: %v", err)
	}
	if resp.Model == "" {
		t.Error("the response names no model")
	}
	if resp.RequestID == "" {
		t.Error("the response carries no request id")
	}

	urgent, err := resp.Answers.Noul("is_urgent")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if urgent.Noul < 0 || urgent.Noul > 1 {
		t.Errorf("Noul = %v, want a probability", urgent.Noul)
	}

	department, err := resp.Answers.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if _, ok := department.Probabilities[department.Choice]; !ok {
		t.Errorf("Choice %q is not among the probabilities %v", department.Choice, department.Probabilities)
	}

	frustration, err := resp.Answers.Score("frustration")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	levels, err := frustration.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	if len(levels) != 3 {
		t.Errorf("the legend holds %d levels, want 3", len(levels))
	}
	if frustration.Score < 0 || frustration.Score > 2 {
		t.Errorf("Score = %v, want a position within the rubric", frustration.Score)
	}

	if resp.Usage.InputTokens == nil {
		t.Error("the response reports no input tokens")
	}

	models, err := client.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("ListModels returned no models")
	}
	for _, model := range models {
		if model.Name == "" {
			t.Errorf("a model entry carries no name: %+v", model)
		}
		if model.ReleaseDate.IsZero() {
			t.Errorf("model %q carries no release date", model.Name)
		}
	}
}
