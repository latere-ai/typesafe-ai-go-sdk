// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

// Command composite-scoring rates a pull request description by breaking one
// judgement into four independent ones.
//
// "Is this description good enough to review?" is not a question a rubric
// answers well: it mixes several concerns, and a single number hides which one
// was weak. Each concern gets its own Score question with its own rubric
// instead. Rubrics differ in length, so every answer is normalised to 0 to 1
// by its own top level before the weights defined in this file combine them.
// The weights live in Go, which is what makes the final number auditable and
// adjustable without touching the questions.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

// requestTimeout bounds the whole evaluation, retries included.
const requestTimeout = 30 * time.Second

// dimension is one atomic judgement. The rubric is held here rather than read
// back from the answer's legend, so the normaliser is a property of the
// program and not of the response.
type dimension struct {
	id           string
	label        string
	weight       float64
	instructions string
	rubric       []any
}

// top is the highest level index, which is the value a perfect answer takes.
// Normalising by it puts rubrics of different lengths on one scale.
func (d dimension) top() float64 { return float64(len(d.rubric) - 1) }

// dimensions is the rubric set, in report order. The weights sum to 1, so the
// composite is itself a 0 to 1 number and reads on the same scale as its
// parts.
var dimensions = []dimension{{
	id:           "problem_stated",
	label:        "problem stated",
	weight:       0.30,
	instructions: "How clearly does the description say what problem this change solves, and why it matters now?",
	rubric: []any{
		"No problem is stated at all",
		"A problem is hinted at, but not named",
		"The problem is named without context",
		"The problem is named with the context that motivates it",
		"The problem, its context and the cost of leaving it are all stated",
	},
}, {
	id:           "change_described",
	label:        "change described",
	weight:       0.25,
	instructions: "How well does the description explain what the change actually does to the code?",
	rubric: []any{
		"No description of the change",
		"A restatement of the title",
		"The main change is described, leaving out the rest",
		"Every meaningful part of the change is described",
	},
}, {
	id:           "test_evidence",
	label:        "test evidence",
	weight:       0.30,
	instructions: "How much evidence is there that the change was verified?",
	rubric: []any{
		"No mention of testing",
		"Testing is claimed without detail",
		"A test or a manual check is named",
		"The tests are named along with what they prove",
		"Tests, results and the failure mode they reproduce are all given",
	},
}, {
	id:           "risk_and_rollback",
	label:        "risk and rollback",
	weight:       0.15,
	instructions: "How well does the description address what could go wrong and how the change would be undone?",
	rubric: []any{
		"Neither risk nor rollback is mentioned",
		"Risk is acknowledged in general terms",
		"The specific risk and the way back out are both stated",
	},
}}

// The bands the composite lands in. They are the reason to compute a single
// number at all: a reviewer queue needs one decision per pull request.
const (
	readyToReview = 0.75
	needsDetail   = 0.50
)

// pullRequest is the state under evaluation. Each part is named so the model
// reads the title, the body and the diff summary as separate facts.
var pullRequest = map[string]any{
	"title": "Add a retry budget to the payments client",
	"files_changed": []any{
		"payments/client.go", "payments/retry.go", "payments/retry_test.go",
	},
	"description": "Payouts to three merchants failed last Friday because the " +
		"upstream processor rate-limited us and our client retried every call " +
		"forever, which held the connection pool until the whole worker stalled. " +
		"This adds a per-request retry budget: at most four attempts, exponential " +
		"backoff with jitter, and Retry-After honoured up to 30s. " +
		"retry_test.go replays Friday's 429 sequence and asserts the budget is " +
		"spent in 6.2s rather than never. The budget is off when MaxRetries is " +
		"zero, so reverting the config change alone restores the old behaviour.",
}

func main() {
	if err := evaluate(context.Background(), os.Stdout); err != nil {
		slog.Error("composite scoring failed", "err", err)
		os.Exit(1)
	}
}

// evaluate builds the client and scores one pull request description. The API
// key comes from TYPESAFE_API_KEY, and TYPESAFE_BASE_URL selects a different
// API root.
func evaluate(ctx context.Context, w io.Writer) error {
	client, err := typesafe.NewClient("")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return run(ctx, client, w)
}

// run scores every dimension in one request and writes the breakdown to w.
func run(ctx context.Context, client *typesafe.Client, w io.Writer) error {
	questions := make(map[string]typesafe.Question, len(dimensions))
	for _, d := range dimensions {
		questions[d.id] = typesafe.Score{Instructions: d.instructions, Criteria: d.rubric}
	}

	resp, err := client.Evaluate(ctx, &typesafe.Request{State: pullRequest, Questions: questions})

	// A refusal the server reported carries the type and the request id that
	// identify the call on the other side.
	var apiErr *typesafe.APIError
	switch {
	case errors.As(err, &apiErr):
		slog.ErrorContext(ctx, "the API refused the evaluation",
			"status", apiErr.StatusCode, "type", apiErr.Type,
			"message", apiErr.Message, "request_id", apiErr.RequestID)
		return err
	case err != nil:
		return err
	}

	report := []string{
		"pull request: " + fmt.Sprint(pullRequest["title"]),
		"",
		fmt.Sprintf("%-18s %6s %5s %11s %7s %13s",
			"dimension", "raw", "of", "normalised", "weight", "contribution"),
	}

	// The dimensions are walked in the order this file declares, not in the
	// map order of the answers, so the report is stable across runs.
	total := 0.0
	for _, d := range dimensions {
		answer, err := resp.Answers.Score(d.id)
		if err != nil {
			return err
		}
		// Every rubric is normalised by its own top level, which is what lets
		// a four-level rubric and a three-level one carry comparable weight.
		normalised := answer.Score / d.top()
		contribution := d.weight * normalised
		total += contribution
		report = append(report, fmt.Sprintf("%-18s %6.2f %5.0f %11.3f %7.2f %13.3f",
			d.label, answer.Score, d.top(), normalised, d.weight, contribution))
	}

	report = append(report, "", fmt.Sprintf("weighted total: %.3f of 1.000", total),
		"verdict:        "+verdictFor(total))
	return writeReport(w, report)
}

// verdictFor turns the composite into the one decision the review queue needs.
func verdictFor(total float64) string {
	switch {
	case total >= readyToReview:
		return "ready for review"
	case total >= needsDetail:
		return "ask the author for the missing detail"
	default:
		return "send back, the description does not describe the change"
	}
}

// writeReport writes the report in one call, so a failed write is reported
// once instead of being checked after every line.
func writeReport(w io.Writer, lines []string) error {
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
