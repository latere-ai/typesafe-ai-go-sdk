// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// The discriminators the API uses for both questions and answers.
const (
	typeNoul   = "noul"
	typeChoice = "choice"
	typeScore  = "score"
)

// Question is one typed question in a Request.
//
// The interface is closed: its method is unexported, so Noul, Choice and Score
// are its only implementations. A value or a pointer to any of the three
// satisfies it.
type Question interface {
	questionType() string
}

// NoulCriteria describes what a yes and a no mean for a Noul question. A nil
// field is left out of the request, which leaves that outcome undescribed.
type NoulCriteria struct {
	// True is what a yes, a value near 1, means. It is a string, a JSON
	// object or a JSON array.
	True any `json:"true,omitzero"`
	// False is what a no, a value near 0, means. It is a string, a JSON
	// object or a JSON array.
	False any `json:"false,omitzero"`
}

// isEmpty reports whether the criteria describe neither outcome.
func (c *NoulCriteria) isEmpty() bool {
	return c == nil || (c.True == nil && c.False == nil)
}

// Noul is a yes/no question. The answer is the probability that the answer is
// yes, from 0 for no to 1 for yes.
//
// The server needs either Instructions or Criteria to know what is being
// asked. A Noul carrying neither is rejected before it is sent.
type Noul struct {
	// Instructions is the question to evaluate, as a string, a JSON object or
	// a JSON array. It is left out of the request when nil.
	Instructions any
	// Criteria describes the two outcomes. It is left out when nil.
	Criteria *NoulCriteria
}

func (Noul) questionType() string { return typeNoul }

// MarshalJSON writes the question with its type discriminator.
func (q Noul) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string        `json:"type"`
		Instructions any           `json:"instructions,omitzero"`
		Criteria     *NoulCriteria `json:"criteria,omitzero"`
	}{typeNoul, q.Instructions, q.Criteria})
}

// Choice selects one option out of a named set. The answer carries the chosen
// option, the probability of every option, and a confidence.
type Choice struct {
	// Instructions is what the model should decide, as a string, a JSON
	// object or a JSON array. It is left out of the request when nil.
	Instructions any
	// Criteria maps each option label to a description of what it covers. A
	// nil value is sent as JSON null, which leaves that option undescribed.
	// The answer's chosen option and the keys of its probabilities are drawn
	// from these labels.
	Criteria map[string]any
}

func (Choice) questionType() string { return typeChoice }

// MarshalJSON writes the question with its type discriminator.
func (q Choice) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string         `json:"type"`
		Instructions any            `json:"instructions,omitzero"`
		Criteria     map[string]any `json:"criteria"`
	}{typeChoice, q.Instructions, q.Criteria})
}

// Score rates the state against an ordered rubric. The answer is a
// probability-weighted position across the levels, which can land between two
// of them.
type Score struct {
	// Instructions is what the model should rate, as a string, a JSON object
	// or a JSON array. It is left out of the request when nil.
	Instructions any
	// Criteria holds the rubric levels in order, indexed from zero. The index
	// of each entry is the score it stands for, and the answer's legend maps
	// those indices back to these entries. At least two levels are required.
	Criteria []any
}

func (Score) questionType() string { return typeScore }

// MarshalJSON writes the question with its type discriminator.
func (q Score) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions,omitzero"`
		Criteria     []any  `json:"criteria"`
	}{typeScore, q.Instructions, q.Criteria})
}

// Request is one evaluation: a single state, and the questions to answer
// about it. Every question sees the same state and is evaluated on its own.
type Request struct {
	// State is the content to evaluate: a string for text, or a JSON object
	// or array for structured data such as chat logs, records or application
	// state. The model reads text only. It is required.
	State any
	// Model is the model that handles the request, either a versioned id such
	// as "jev-1.13.0" or an alias such as "jev-latest". When empty, the
	// client's default model is sent.
	Model string
	// Questions holds the questions to answer, under ids chosen by the
	// caller. The answers come back under the same ids. An id is not sent to
	// the underlying model and takes no part in inference. At least one
	// question is required.
	Questions map[string]Question
}

// encodeRequest renders a request as the POST body, substituting the client's
// default model for an empty Model. Evaluate and the contract tests both go
// through it, so what is tested is what is sent.
func encodeRequest(req *Request, defaultModel string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	body, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{req.State, model, req.Questions})
	if err != nil {
		return nil, fmt.Errorf("typesafe: encoding the request: %w", err)
	}
	return body, nil
}

// validate rejects a request the server would reject, naming the field that is
// wrong. Questions are checked in id order, so the same request always names
// the same field first.
func (r *Request) validate() error {
	if r == nil {
		return &ValidationError{Field: "request", Message: "must not be nil"}
	}
	if r.State == nil {
		return &ValidationError{Field: "state", Message: "must not be nil"}
	}
	if len(r.Questions) == 0 {
		return &ValidationError{Field: "questions", Message: "must hold at least one question"}
	}
	for _, id := range slices.Sorted(maps.Keys(r.Questions)) {
		if err := validateQuestion(r.Questions[id], "questions["+id+"]"); err != nil {
			return err
		}
	}
	return nil
}

// validateQuestion checks one question, under the field path it sits at.
func validateQuestion(q Question, field string) error {
	switch q := q.(type) {
	case nil:
		return &ValidationError{Field: field, Message: "must not be nil"}
	case Noul:
		return validateNoul(q, field)
	case *Noul:
		if q == nil {
			return &ValidationError{Field: field, Message: "must not be nil"}
		}
		return validateNoul(*q, field)
	case Choice:
		return validateChoice(q, field)
	case *Choice:
		if q == nil {
			return &ValidationError{Field: field, Message: "must not be nil"}
		}
		return validateChoice(*q, field)
	case Score:
		return validateScore(q, field)
	case *Score:
		if q == nil {
			return &ValidationError{Field: field, Message: "must not be nil"}
		}
		return validateScore(*q, field)
	default:
		return &ValidationError{Field: field, Message: "is not a Noul, Choice or Score question"}
	}
}

// validateNoul enforces the server's rule that a yes/no question says what is
// being asked, through its instructions, its criteria, or both.
func validateNoul(q Noul, field string) error {
	if q.Instructions == nil && q.Criteria.isEmpty() {
		return &ValidationError{Field: field, Message: "must set Instructions or Criteria"}
	}
	return nil
}

// validateChoice enforces that there is something to choose between.
func validateChoice(q Choice, field string) error {
	if len(q.Criteria) == 0 {
		return &ValidationError{Field: field + ".Criteria", Message: "must hold at least one option"}
	}
	return nil
}

// validateScore enforces the documented minimum of two levels and rejects an
// undescribed level, which the server answers with a validation failure.
func validateScore(q Score, field string) error {
	if len(q.Criteria) < 2 {
		return &ValidationError{Field: field + ".Criteria", Message: "must hold at least two levels"}
	}
	for i, level := range q.Criteria {
		if level == nil {
			return &ValidationError{
				Field:   fmt.Sprintf("%s.Criteria[%d]", field, i),
				Message: "must not be nil",
			}
		}
	}
	return nil
}
