// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

// Answer is one question's result. The interface is closed: its method is
// unexported, so the four types in this package are its only implementations.
type Answer interface {
	answerType() string
}

// NoulAnswer is the result of a Noul question. It carries no confidence: the
// value is itself the model's degree of belief.
type NoulAnswer struct {
	// Noul is the yes/no answer, from 0 for no to 1 for yes.
	Noul float64 `json:"noul"`
}

func (NoulAnswer) answerType() string { return typeNoul }

// ChoiceAnswer is the result of a Choice question.
type ChoiceAnswer struct {
	// Choice is the highest-probability option, one of the option labels the
	// question defined.
	Choice string `json:"choice"`
	// Probabilities maps every option label to its probability. The values
	// sum to 1.
	Probabilities map[string]float64 `json:"probabilities"`
	// Confidence is how concentrated the distribution in Probabilities is,
	// from 0 to 1.
	Confidence float64 `json:"confidence"`
}

func (ChoiceAnswer) answerType() string { return typeChoice }

// ScoreAnswer is the result of a Score question.
type ScoreAnswer struct {
	// Score is the probability-weighted position across the rubric levels. It
	// can fall between two levels.
	Score float64 `json:"score"`
	// Legend maps each level index, as a decimal string, back to the level
	// description sent in the question's criteria.
	Legend map[string]any `json:"legend"`
	// Probabilities maps each level index, as a decimal string, to its
	// probability. The values sum to 1.
	Probabilities map[string]float64 `json:"probabilities"`
	// Confidence is how concentrated the distribution in Probabilities is,
	// from 0 to 1.
	Confidence float64 `json:"confidence"`
}

func (ScoreAnswer) answerType() string { return typeScore }

// Levels returns the legend descriptions ordered by their level index, which
// is the order the rubric was sent in. It fails when a legend key is not a
// decimal index.
func (a ScoreAnswer) Levels() ([]any, error) {
	type level struct {
		index int
		key   string
	}
	levels := make([]level, 0, len(a.Legend))
	for key := range a.Legend {
		index, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("typesafe: legend key %q is not a level index: %w", key, err)
		}
		levels = append(levels, level{index: index, key: key})
	}
	slices.SortFunc(levels, func(x, y level) int { return cmp.Compare(x.index, y.index) })
	out := make([]any, 0, len(levels))
	for _, l := range levels {
		out = append(out, a.Legend[l.key])
	}
	return out, nil
}

// UnknownAnswer is an answer whose type this package does not model. The API
// carries more question types than the ones documented here, and it can gain
// more at any time, so an unrecognised type is carried through as raw JSON
// instead of failing the whole response.
type UnknownAnswer struct {
	// Type is the value of the answer's type field.
	Type string
	// Raw is the answer object as it arrived.
	Raw json.RawMessage
}

func (a UnknownAnswer) answerType() string { return a.Type }

// Answers holds one answer per question, under the ids the request used.
type Answers map[string]Answer

// UnmarshalJSON decodes each answer into the type its discriminator names.
func (a *Answers) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("typesafe: decoding answers: %w", err)
	}
	if raw == nil {
		*a = nil
		return nil
	}
	out := make(Answers, len(raw))
	for id, message := range raw {
		answer, err := decodeAnswer(message)
		if err != nil {
			return fmt.Errorf("typesafe: decoding answer %q: %w", id, err)
		}
		out[id] = answer
	}
	*a = out
	return nil
}

// decodeAnswer reads one answer object, dispatching on its type field.
func decodeAnswer(message json.RawMessage) (Answer, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case typeNoul:
		var answer NoulAnswer
		if err := json.Unmarshal(message, &answer); err != nil {
			return nil, err
		}
		return answer, nil
	case typeChoice:
		var answer ChoiceAnswer
		if err := json.Unmarshal(message, &answer); err != nil {
			return nil, err
		}
		return answer, nil
	case typeScore:
		var answer ScoreAnswer
		if err := json.Unmarshal(message, &answer); err != nil {
			return nil, err
		}
		return answer, nil
	default:
		return UnknownAnswer{Type: probe.Type, Raw: slices.Clone(message)}, nil
	}
}

// Noul returns the answer under id as a NoulAnswer. It fails when no answer
// carries that id, or when the answer is of another type.
func (a Answers) Noul(id string) (NoulAnswer, error) {
	return answerOf[NoulAnswer](a, id, typeNoul)
}

// Choice returns the answer under id as a ChoiceAnswer. It fails when no
// answer carries that id, or when the answer is of another type.
func (a Answers) Choice(id string) (ChoiceAnswer, error) {
	return answerOf[ChoiceAnswer](a, id, typeChoice)
}

// Score returns the answer under id as a ScoreAnswer. It fails when no answer
// carries that id, or when the answer is of another type.
func (a Answers) Score(id string) (ScoreAnswer, error) {
	return answerOf[ScoreAnswer](a, id, typeScore)
}

// answerOf looks one answer up and asserts its type, naming both the id and
// the type that was found when the assertion fails.
func answerOf[T Answer](answers Answers, id, want string) (T, error) {
	var zero T
	answer, ok := answers[id]
	if !ok {
		return zero, fmt.Errorf("typesafe: no answer under id %q", id)
	}
	if answer == nil {
		return zero, fmt.Errorf("typesafe: the answer under id %q is nil", id)
	}
	typed, ok := answer.(T)
	if !ok {
		return zero, fmt.Errorf("typesafe: the answer under id %q has type %q, not %q", id, answer.answerType(), want)
	}
	return typed, nil
}

// Usage is the token count for one request. Input tokens are billed, output
// tokens are not. Both fields are pointers because the API marks both
// optional: a nil field is one the server did not report, which a zero count
// would be indistinguishable from.
type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

// Response is the result of one evaluation.
type Response struct {
	// Model is the versioned id of the model that performed the evaluation.
	// A request that sent an alias such as "jev-latest" gets back the version
	// the alias resolved to, such as "jev-1.13.0".
	Model string `json:"model"`
	// Answers holds one answer per question, under the ids the request used.
	Answers Answers `json:"answers"`
	// Usage is the token count for the request.
	Usage Usage `json:"usage"`
	// RequestID is the value of the x-typesafe-request-id response header,
	// which identifies the request in the server's own records. It is not
	// part of the response body.
	RequestID string `json:"-"`
}
