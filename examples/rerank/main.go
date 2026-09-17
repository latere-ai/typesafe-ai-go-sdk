// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

// Command rerank reorders a keyword-search shortlist by relevance.
//
// A keyword index returns passages that share words with the query, which is
// not the same as passages that answer it. Re-ranking scores every candidate
// against the query and sorts by that score. All eight candidates are scored
// in one request: the query and the whole shortlist go in the state, and each
// question names one candidate by its id, so the answers come back under those
// same ids and pair with the shortlist without any bookkeeping.
//
// The score is a Noul rather than a Score. A yes/no question answered with a
// probability is already a continuous ranking signal, and it needs no rubric.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

// requestTimeout bounds the whole evaluation, retries included.
const requestTimeout = 30 * time.Second

// snippetWidth is how much of a passage the report shows per line.
const snippetWidth = 62

// query is what the user asked. It is part of the state, because relevance is
// a judgement about the pair rather than about the passage alone.
const query = "How do I rotate the production database password without downtime?"

// candidate is one passage on the shortlist. The id is both the key in the
// state and the question id, which is what pairs an answer back to a passage.
type candidate struct {
	id   string
	text string
}

// shortlist is what the keyword index returned, in the order it returned it.
var shortlist = []candidate{
	{"cand_1", "Database passwords are stored in the secret manager under the " +
		"db/ prefix. Access is granted per service account and audited monthly."},
	{"cand_2", "To restart the production database, drain the connection pool " +
		"first, then issue a restart through the operator. Expect 40s of downtime."},
	{"cand_3", "Rotate a credential by adding the new password as a second " +
		"accepted secret, waiting for every pod to pick it up on its next " +
		"refresh, and only then retiring the old one. No connection is dropped."},
	{"cand_4", "The password policy requires 24 characters, rotation every 90 " +
		"days, and no reuse across environments."},
	{"cand_5", "Downtime during a deploy usually comes from a rollout that " +
		"terminates pods faster than the load balancer drains them."},
	{"cand_6", "The connection pool reloads its credentials when the mounted " +
		"secret changes, without reconnecting existing sessions. This is what " +
		"makes a rolling credential change invisible to running queries."},
	{"cand_7", "Production access requires a break-glass ticket. The ticket " +
		"records who rotated which secret and why."},
	{"cand_8", "Staging databases are reset nightly, so a password set there " +
		"does not survive to the next morning."},
}

func main() {
	if err := evaluate(context.Background(), os.Stdout); err != nil {
		slog.Error("rerank failed", "err", err)
		os.Exit(1)
	}
}

// evaluate builds the client and re-ranks the shortlist. The API key comes
// from TYPESAFE_API_KEY, and TYPESAFE_BASE_URL selects a different API root.
func evaluate(ctx context.Context, w io.Writer) error {
	client, err := typesafe.NewClient("")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return run(ctx, client, w)
}

// ranked is one candidate with the relevance it was scored at and the position
// the keyword index gave it.
type ranked struct {
	candidate candidate
	relevance float64
	wasRank   int
}

// run scores the whole shortlist in one request and writes the new order to w.
func run(ctx context.Context, client *typesafe.Client, w io.Writer) error {
	// The state carries the query and every candidate. Each question then
	// names one candidate, so all eight judgements read the same material.
	passages := make([]any, 0, len(shortlist))
	questions := make(map[string]typesafe.Question, len(shortlist))
	for _, c := range shortlist {
		passages = append(passages, map[string]any{"id": c.id, "text": c.text})
		questions[c.id] = typesafe.Noul{
			Instructions: "Passage " + c.id + " answers the question in the query.",
			Criteria: &typesafe.NoulCriteria{
				True: "The passage states the procedure the query asks for, or a " +
					"fact the reader needs to carry it out",
				False: "The passage is about a neighbouring topic, or mentions the " +
					"same words without answering the query",
			},
		}
	}

	resp, err := client.Evaluate(ctx, &typesafe.Request{
		State:     map[string]any{"query": query, "candidates": passages},
		Questions: questions,
	})

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

	// The shortlist is walked in index order, not the answer map's order, so
	// the original rank is the loop index and the result is deterministic.
	order := make([]ranked, 0, len(shortlist))
	for i, c := range shortlist {
		answer, err := resp.Answers.Noul(c.id)
		if err != nil {
			return err
		}
		order = append(order, ranked{candidate: c, relevance: answer.Noul, wasRank: i + 1})
	}

	// A stable sort keeps two equally relevant passages in the order the
	// keyword index put them, which makes the report reproducible.
	slices.SortStableFunc(order, func(x, y ranked) int {
		return cmp.Compare(y.relevance, x.relevance)
	})

	report := []string{
		"query: " + query,
		"",
		fmt.Sprintf("%4s %4s %10s  %-8s %s", "rank", "was", "relevance", "id", "passage"),
	}
	for i, r := range order {
		report = append(report, fmt.Sprintf("%4d %4d %10.2f  %-8s %s",
			i+1, r.wasRank, r.relevance, r.candidate.id, snippet(r.candidate.text, snippetWidth)))
	}
	report = append(report, "", "top hit moved up from position "+fmt.Sprint(order[0].wasRank))
	return writeReport(w, report)
}

// snippet shortens a passage to one line of the report.
func snippet(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:width-3]) + "..."
}

// writeReport writes the report in one call, so a failed write is reported
// once instead of being checked after every line.
func writeReport(w io.Writer, lines []string) error {
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
