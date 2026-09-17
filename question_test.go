// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// jsonValue decodes JSON into the generic shape used to compare two encodings
// without depending on key order.
func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return value
}

func TestQuestionMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		question Question
		want     string
	}{
		{
			name:     "noul with instructions only",
			question: Noul{Instructions: "Does this convey urgency?"},
			want:     `{"type":"noul","instructions":"Does this convey urgency?"}`,
		},
		{
			name: "noul with both criteria",
			question: Noul{
				Instructions: "Does this convey urgency?",
				Criteria:     &NoulCriteria{True: "Explicitly time-sensitive", False: "No urgency expressed"},
			},
			want: `{"type":"noul","instructions":"Does this convey urgency?","criteria":{"true":"Explicitly time-sensitive","false":"No urgency expressed"}}`,
		},
		{
			name:     "noul criteria drop the undescribed outcome",
			question: Noul{Criteria: &NoulCriteria{True: "yes"}},
			want:     `{"type":"noul","criteria":{"true":"yes"}}`,
		},
		{
			name:     "noul with structured instructions",
			question: Noul{Instructions: map[string]any{"ask": "urgent?"}},
			want:     `{"type":"noul","instructions":{"ask":"urgent?"}}`,
		},
		{
			name:     "choice",
			question: Choice{Instructions: "Which team?", Criteria: map[string]any{"billing": "Payments"}},
			want:     `{"type":"choice","instructions":"Which team?","criteria":{"billing":"Payments"}}`,
		},
		{
			name:     "choice option with no description",
			question: Choice{Criteria: map[string]any{"billing": nil}},
			want:     `{"type":"choice","criteria":{"billing":null}}`,
		},
		{
			name:     "score",
			question: Score{Instructions: "How frustrated?", Criteria: []any{"Calm", "Very angry"}},
			want:     `{"type":"score","instructions":"How frustrated?","criteria":["Calm","Very angry"]}`,
		},
		{
			name:     "score with structured levels",
			question: Score{Criteria: []any{[]any{"a"}, map[string]any{"b": 1.0}}},
			want:     `{"type":"score","criteria":[["a"],{"b":1}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.question)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if !reflect.DeepEqual(jsonValue(t, got), jsonValue(t, []byte(tt.want))) {
				t.Errorf("marshalled to %s, want %s", got, tt.want)
			}
		})
	}
}

// An empty string is a description the caller chose, not an absent field, so
// it has to survive encoding. omitempty would drop it; omitzero does not.
func TestEmptyStringInstructionsSurvive(t *testing.T) {
	for _, question := range []Question{
		Noul{Instructions: "", Criteria: &NoulCriteria{True: ""}},
		Choice{Instructions: "", Criteria: map[string]any{"a": ""}},
		Score{Instructions: "", Criteria: []any{"", ""}},
	} {
		data, err := json.Marshal(question)
		if err != nil {
			t.Fatalf("marshalling %T: %v", question, err)
		}
		decoded, ok := jsonValue(t, data).(map[string]any)
		if !ok {
			t.Fatalf("%T did not marshal to an object", question)
		}
		if _, present := decoded["instructions"]; !present {
			t.Errorf("%T dropped an empty instructions value: %s", question, data)
		}
	}
}

func TestQuestionType(t *testing.T) {
	tests := []struct {
		question Question
		want     string
	}{
		{Noul{}, typeNoul},
		{&Noul{}, typeNoul},
		{Choice{}, typeChoice},
		{&Choice{}, typeChoice},
		{Score{}, typeScore},
		{&Score{}, typeScore},
	}
	for _, tt := range tests {
		if got := tt.question.questionType(); got != tt.want {
			t.Errorf("%T.questionType() = %q, want %q", tt.question, got, tt.want)
		}
	}
}

func TestEncodeRequestFillsTheDefaultModel(t *testing.T) {
	body, err := encodeRequest(&Request{
		State:     "hi",
		Questions: map[string]Question{"q": Noul{Instructions: "?"}},
	}, "jev-latest")
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	decoded, ok := jsonValue(t, body).(map[string]any)
	if !ok {
		t.Fatalf("request did not encode to an object: %s", body)
	}
	if decoded["model"] != "jev-latest" {
		t.Errorf("model = %v, want jev-latest", decoded["model"])
	}
}

func TestEncodeRequestKeepsAnExplicitModel(t *testing.T) {
	body, err := encodeRequest(&Request{
		State:     "hi",
		Model:     "jev-1.13.0",
		Questions: map[string]Question{"q": Noul{Instructions: "?"}},
	}, "jev-latest")
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	decoded, ok := jsonValue(t, body).(map[string]any)
	if !ok {
		t.Fatalf("request did not encode to an object: %s", body)
	}
	if decoded["model"] != "jev-1.13.0" {
		t.Errorf("model = %v, want jev-1.13.0", decoded["model"])
	}
}

func TestEncodeRequestFailsOnAnUnencodableState(t *testing.T) {
	_, err := encodeRequest(&Request{
		State:     make(chan int),
		Questions: map[string]Question{"q": Noul{Instructions: "?"}},
	}, "jev-latest")
	if err == nil {
		t.Fatal("encoding a channel state should fail")
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Errorf("error %v does not unwrap to the encoder's own error", err)
	}
}

// stubQuestion stands in for a question type this package does not know, which
// only an implementation inside the package can be.
type stubQuestion struct{}

func (stubQuestion) questionType() string { return "stub" }

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name      string
		request   *Request
		wantField string
	}{
		{name: "nil request", request: nil, wantField: "request"},
		{
			name:      "nil state",
			request:   &Request{Questions: map[string]Question{"q": Noul{Instructions: "?"}}},
			wantField: "state",
		},
		{name: "no questions", request: &Request{State: "hi"}, wantField: "questions"},
		{
			name:      "empty questions map",
			request:   &Request{State: "hi", Questions: map[string]Question{}},
			wantField: "questions",
		},
		{
			name:      "nil question",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": nil}},
			wantField: "questions[q]",
		},
		{
			name:      "nil noul pointer",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": (*Noul)(nil)}},
			wantField: "questions[q]",
		},
		{
			name:      "nil choice pointer",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": (*Choice)(nil)}},
			wantField: "questions[q]",
		},
		{
			name:      "nil score pointer",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": (*Score)(nil)}},
			wantField: "questions[q]",
		},
		{
			name:      "noul with neither instructions nor criteria",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": Noul{}}},
			wantField: "questions[q]",
		},
		{
			name:      "noul with empty criteria",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": &Noul{Criteria: &NoulCriteria{}}}},
			wantField: "questions[q]",
		},
		{
			name:      "choice with no options",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": Choice{Instructions: "?"}}},
			wantField: "questions[q].Criteria",
		},
		{
			name:      "choice pointer with no options",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": &Choice{}}},
			wantField: "questions[q].Criteria",
		},
		{
			name:      "score with one level",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": Score{Criteria: []any{"Calm"}}}},
			wantField: "questions[q].Criteria",
		},
		{
			name:      "score pointer with no levels",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": &Score{}}},
			wantField: "questions[q].Criteria",
		},
		{
			name:      "score with a nil level",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": Score{Criteria: []any{"Calm", nil}}}},
			wantField: "questions[q].Criteria[1]",
		},
		{
			name:      "unknown question type",
			request:   &Request{State: "hi", Questions: map[string]Question{"q": stubQuestion{}}},
			wantField: "questions[q]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.validate()
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("validate() = %v, want a *ValidationError", err)
			}
			if invalid.Field != tt.wantField {
				t.Errorf("Field = %q, want %q", invalid.Field, tt.wantField)
			}
			if invalid.Message == "" {
				t.Error("Message is empty")
			}
		})
	}
}

func TestRequestValidateAccepts(t *testing.T) {
	requests := []*Request{
		{State: "hi", Questions: map[string]Question{"q": Noul{Instructions: "?"}}},
		{State: "hi", Questions: map[string]Question{"q": Noul{Criteria: &NoulCriteria{False: "no"}}}},
		{State: map[string]any{"a": 1}, Questions: map[string]Question{"q": &Noul{Instructions: "?"}}},
		{State: []any{"a"}, Questions: map[string]Question{"q": Choice{Criteria: map[string]any{"a": nil}}}},
		{State: "hi", Questions: map[string]Question{"q": Score{Criteria: []any{"a", "b"}}}},
		{State: "hi", Questions: map[string]Question{"q": &Score{Criteria: []any{"a", "b"}}}},
		{State: "hi", Questions: map[string]Question{"q": &Choice{Criteria: map[string]any{"a": "b"}}}},
	}
	for i, request := range requests {
		if err := request.validate(); err != nil {
			t.Errorf("request %d: validate() = %v, want nil", i, err)
		}
	}
}

// The first offending question is the same one every time, whatever order the
// map happens to iterate in.
func TestRequestValidateReportsQuestionsInIDOrder(t *testing.T) {
	request := &Request{
		State: "hi",
		Questions: map[string]Question{
			"z": Choice{},
			"a": Score{},
			"m": Noul{},
		},
	}
	for range 50 {
		err := request.validate()
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("validate() = %v, want a *ValidationError", err)
		}
		if invalid.Field != "questions[a].Criteria" {
			t.Fatalf("Field = %q, want questions[a].Criteria", invalid.Field)
		}
	}
}
