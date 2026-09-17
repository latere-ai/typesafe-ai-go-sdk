// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAnswersUnmarshalDispatchesOnType(t *testing.T) {
	body := `{
		"n": {"type": "noul", "noul": 0.92},
		"c": {"type": "choice", "choice": "technical", "probabilities": {"technical": 0.85}, "confidence": 0.82},
		"s": {"type": "score", "score": 1.6, "legend": {"0": "Calm"}, "probabilities": {"0": 1.0}, "confidence": 0.78},
		"u": {"type": "bounding_box", "box": [1, 2, 3, 4]}
	}`
	var answers Answers
	if err := json.Unmarshal([]byte(body), &answers); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(answers) != 4 {
		t.Fatalf("decoded %d answers, want 4", len(answers))
	}

	noul, err := answers.Noul("n")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if noul.Noul != 0.92 {
		t.Errorf("Noul = %v, want 0.92", noul.Noul)
	}

	choice, err := answers.Choice("c")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if choice.Choice != "technical" || choice.Confidence != 0.82 || choice.Probabilities["technical"] != 0.85 {
		t.Errorf("choice answer decoded as %+v", choice)
	}

	score, err := answers.Score("s")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Score != 1.6 || score.Confidence != 0.78 || score.Legend["0"] != "Calm" {
		t.Errorf("score answer decoded as %+v", score)
	}

	unknown, ok := answers["u"].(UnknownAnswer)
	if !ok {
		t.Fatalf("answer u decoded as %T, want UnknownAnswer", answers["u"])
	}
	if unknown.Type != "bounding_box" || unknown.answerType() != "bounding_box" {
		t.Errorf("UnknownAnswer.Type = %q, want bounding_box", unknown.Type)
	}
	var raw map[string]any
	if err := json.Unmarshal(unknown.Raw, &raw); err != nil {
		t.Fatalf("UnknownAnswer.Raw is not the answer object: %v", err)
	}
	if raw["type"] != "bounding_box" {
		t.Errorf("UnknownAnswer.Raw = %s, want the whole answer object", unknown.Raw)
	}
}

func TestAnswersUnmarshalNull(t *testing.T) {
	answers := Answers{"stale": NoulAnswer{}}
	if err := json.Unmarshal([]byte("null"), &answers); err != nil {
		t.Fatalf("decoding null: %v", err)
	}
	if answers != nil {
		t.Errorf("decoded null to %v, want nil", answers)
	}
}

func TestAnswersUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "not an object", body: `["a"]`, want: "decoding answers"},
		{name: "answer is not an object", body: `{"q": 7}`, want: `decoding answer "q"`},
		{name: "answer body does not fit its type", body: `{"q": {"type":"noul","noul":"x"}}`, want: `decoding answer "q"`},
		{name: "choice body does not fit its type", body: `{"q": {"type":"choice","choice":1}}`, want: `decoding answer "q"`},
		{name: "score body does not fit its type", body: `{"q": {"type":"score","score":"x"}}`, want: `decoding answer "q"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var answers Answers
			err := json.Unmarshal([]byte(tt.body), &answers)
			if err == nil {
				t.Fatal("decoding should have failed")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %q", err, tt.want)
			}
		})
	}
}

func TestAnswerAccessorErrors(t *testing.T) {
	answers := Answers{
		"n":   NoulAnswer{Noul: 1},
		"nil": nil,
	}
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "missing id",
			call: func() error { _, err := answers.Noul("absent"); return err },
			want: `no answer under id "absent"`,
		},
		{
			name: "wrong type for choice",
			call: func() error { _, err := answers.Choice("n"); return err },
			want: `the answer under id "n" has type "noul", not "choice"`,
		},
		{
			name: "wrong type for score",
			call: func() error { _, err := answers.Score("n"); return err },
			want: `the answer under id "n" has type "noul", not "score"`,
		},
		{
			name: "nil answer value",
			call: func() error { _, err := answers.Noul("nil"); return err },
			want: `the answer under id "nil" is nil`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not say %q", err, tt.want)
			}
		})
	}
}

func TestScoreAnswerLevels(t *testing.T) {
	answer := ScoreAnswer{Legend: map[string]any{
		"2":  "Very angry",
		"10": "Beyond angry",
		"0":  "Calm",
		"1":  "Frustrated",
	}}
	levels, err := answer.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	want := []any{"Calm", "Frustrated", "Very angry", "Beyond angry"}
	if !reflect.DeepEqual(levels, want) {
		t.Errorf("Levels() = %v, want %v", levels, want)
	}
}

func TestScoreAnswerLevelsEmptyLegend(t *testing.T) {
	levels, err := ScoreAnswer{}.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	if len(levels) != 0 {
		t.Errorf("Levels() = %v, want none", levels)
	}
}

func TestScoreAnswerLevelsRejectsANonIndexKey(t *testing.T) {
	_, err := ScoreAnswer{Legend: map[string]any{"calm": "Calm"}}.Levels()
	if err == nil {
		t.Fatal("a legend key that is not an index should fail")
	}
	if !strings.Contains(err.Error(), `legend key "calm"`) {
		t.Errorf("error %q does not name the key", err)
	}
}

func TestAnswerType(t *testing.T) {
	tests := []struct {
		answer Answer
		want   string
	}{
		{NoulAnswer{}, typeNoul},
		{ChoiceAnswer{}, typeChoice},
		{ScoreAnswer{}, typeScore},
		{UnknownAnswer{Type: "other"}, "other"},
	}
	for _, tt := range tests {
		if got := tt.answer.answerType(); got != tt.want {
			t.Errorf("%T.answerType() = %q, want %q", tt.answer, got, tt.want)
		}
	}
}

func TestUsageDistinguishesAbsentFromZero(t *testing.T) {
	var reported struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(`{"usage":{"input_tokens":0}}`), &reported); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if reported.Usage.InputTokens == nil || *reported.Usage.InputTokens != 0 {
		t.Errorf("InputTokens = %v, want a pointer to 0", reported.Usage.InputTokens)
	}
	if reported.Usage.OutputTokens != nil {
		t.Errorf("OutputTokens = %v, want nil for an unreported count", *reported.Usage.OutputTokens)
	}
}
