// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The state every documented example evaluates.
const documentedState = "Help! My payouts have been failing for 3 days."

// TestRequestsMatchTheDocumentedExamples encodes the SDK's own request types
// and compares each against the request the API reference prints, so a change
// to the encoding that drifts from the wire contract fails here.
func TestRequestsMatchTheDocumentedExamples(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		request *Request
	}{
		{
			name: "noul",
			file: "request_noul.json",
			request: &Request{
				State: documentedState,
				Questions: map[string]Question{
					"is_urgent": Noul{Instructions: "Does this convey urgency?"},
				},
			},
		},
		{
			name: "noul with criteria",
			file: "request_noul_criteria.json",
			request: &Request{
				State: documentedState,
				Model: "jev-latest",
				Questions: map[string]Question{
					"is_urgent": Noul{
						Instructions: "Does this convey urgency?",
						Criteria: &NoulCriteria{
							True:  "Explicitly time-sensitive",
							False: "No urgency expressed",
						},
					},
				},
			},
		},
		{
			name: "choice",
			file: "request_choice.json",
			request: &Request{
				State: documentedState,
				Questions: map[string]Question{
					"department": Choice{
						Instructions: "Which team should handle this?",
						Criteria: map[string]any{
							"billing":   "Payments, invoicing, refunds",
							"technical": "Bugs, outages, integrations",
							"sales":     "Pricing, upgrades, new accounts",
						},
					},
				},
			},
		},
		{
			name: "score",
			file: "request_score.json",
			request: &Request{
				State: documentedState,
				Questions: map[string]Question{
					"frustration": Score{
						Instructions: "How frustrated is the customer?",
						Criteria:     []any{"Calm", "Frustrated", "Very angry"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.request.validate(); err != nil {
				t.Fatalf("a documented request failed validation: %v", err)
			}
			encoded, err := encodeRequest(tt.request, DefaultModel)
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			want := fixture(t, tt.file)
			if !reflect.DeepEqual(jsonValue(t, encoded), jsonValue(t, want)) {
				t.Errorf("encoded\n%s\nwant\n%s", encoded, want)
			}
		})
	}
}

func TestDocumentedNoulResponseDecodes(t *testing.T) {
	response := decodeResponse(t, "response_noul.json")
	if response.Model != "jev-latest" {
		t.Errorf("Model = %q", response.Model)
	}
	answer, err := response.Answers.Noul("is_urgent")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if answer.Noul != 0.92 {
		t.Errorf("Noul = %v, want 0.92", answer.Noul)
	}
}

func TestDocumentedChoiceResponseDecodes(t *testing.T) {
	response := decodeResponse(t, "response_choice.json")
	answer, err := response.Answers.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if answer.Choice != "technical" {
		t.Errorf("Choice = %q, want technical", answer.Choice)
	}
	if answer.Confidence != 0.82 {
		t.Errorf("Confidence = %v, want 0.82", answer.Confidence)
	}
	want := map[string]float64{"billing": 0.08, "technical": 0.85, "sales": 0.07}
	if !reflect.DeepEqual(answer.Probabilities, want) {
		t.Errorf("Probabilities = %v, want %v", answer.Probabilities, want)
	}
}

func TestDocumentedScoreResponseDecodes(t *testing.T) {
	response := decodeResponse(t, "response_score.json")
	answer, err := response.Answers.Score("frustration")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if answer.Score != 1.6 || answer.Confidence != 0.78 {
		t.Errorf("score answer decoded as %+v", answer)
	}
	levels, err := answer.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	if !reflect.DeepEqual(levels, []any{"Calm", "Frustrated", "Very angry"}) {
		t.Errorf("Levels() = %v", levels)
	}
	want := map[string]float64{"0": 0.05, "1": 0.3, "2": 0.65}
	if !reflect.DeepEqual(answer.Probabilities, want) {
		t.Errorf("Probabilities = %v, want %v", answer.Probabilities, want)
	}
}

// The observed response carries all three answer types at once and reports the
// version the requested alias resolved to.
func TestObservedResponseDecodes(t *testing.T) {
	response := decodeResponse(t, "response_all.json")
	if response.Model != "jev-1.13.0" {
		t.Errorf("Model = %q, want the resolved version", response.Model)
	}
	if len(response.Answers) != 3 {
		t.Fatalf("decoded %d answers, want 3", len(response.Answers))
	}
	if _, err := response.Answers.Noul("is_urgent"); err != nil {
		t.Errorf("Noul: %v", err)
	}
	choice, err := response.Answers.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if choice.Choice != "billing" {
		t.Errorf("Choice = %q", choice.Choice)
	}
	score, err := response.Answers.Score("frustration")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Score != 1.03 {
		t.Errorf("Score = %v", score.Score)
	}
	if response.Usage.InputTokens == nil || *response.Usage.InputTokens != 378 {
		t.Errorf("InputTokens = %v, want 378", response.Usage.InputTokens)
	}
	if response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 73 {
		t.Errorf("OutputTokens = %v, want 73", response.Usage.OutputTokens)
	}
}

// decodeResponse decodes one response fixture.
func decodeResponse(t *testing.T, name string) Response {
	t.Helper()
	var response Response
	if err := json.Unmarshal(fixture(t, name), &response); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return response
}
