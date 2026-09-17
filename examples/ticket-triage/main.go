// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

// Command ticket-triage routes one support ticket with a single evaluation.
//
// Every question the decision tree can reach is sent in one request, including
// the ones that only matter for some categories. Questions are answered in
// parallel, so a speculative question costs no extra round trip, and the code
// reads only the answers its branch needs. The category is then gated on its
// confidence: a confident classification is acted on, an unconfident one goes
// to a human.
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

// requestTimeout bounds the whole evaluation, retries included. The client
// applies its own per-attempt timeout inside this budget.
const requestTimeout = 30 * time.Second

// autoRouteConfidence is the confidence the category answer must clear before
// the ticket is routed without a human looking at it. Misrouting a ticket
// costs a handoff rather than money, so the bar sits below the one a
// destructive action would need.
const autoRouteConfidence = 0.75

// ticket is the state sent for evaluation. A map keeps each part named, so the
// model reads the subject, the body and the plan as separate facts rather than
// as one flattened block of text.
var ticket = map[string]any{
	"subject":       "Charged twice for order #98423, and now I cannot log in",
	"customer_plan": "business",
	"body": "Hi, I placed an order (#98423) last Thursday and my card was " +
		"charged twice for it. I have tried to sort this out in the portal " +
		"but I cannot log in since the site update on Monday: the password " +
		"reset email never arrives. Please refund the duplicate charge. " +
		"This is the second time this month and it is getting frustrating.",
}

func main() {
	if err := evaluate(context.Background(), os.Stdout); err != nil {
		slog.Error("ticket triage failed", "err", err)
		os.Exit(1)
	}
}

// evaluate builds the client and runs one triage pass. The API key comes from
// TYPESAFE_API_KEY, and TYPESAFE_BASE_URL selects a different API root.
func evaluate(ctx context.Context, w io.Writer) error {
	client, err := typesafe.NewClient("")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return run(ctx, client, w)
}

// run evaluates the ticket and writes the routing decision to w.
func run(ctx context.Context, client *typesafe.Client, w io.Writer) error {
	resp, err := client.Evaluate(ctx, &typesafe.Request{
		State: ticket,
		Questions: map[string]typesafe.Question{
			// category drives the branch. Every question after it is read only
			// when the branch it belongs to is taken.
			"category": typesafe.Choice{
				Instructions: "Which queue should handle this support ticket?",
				Criteria: map[string]any{
					"billing":         "Charges, invoices, refunds, subscriptions",
					"bug_report":      "Something is broken or produces errors",
					"account":         "Login, permissions, profile, security",
					"feature_request": "The customer asks for new functionality",
				},
			},
			// refund_requested matters for billing only.
			"refund_requested": typesafe.Noul{
				Instructions: "The customer explicitly asks for a refund or a credit.",
				Criteria: &typesafe.NoulCriteria{
					True:  "A refund, a credit or a chargeback is asked for in so many words",
					False: "No money is asked back, only an explanation or a fix",
				},
			},
			// severity matters for bug_report only.
			"severity": typesafe.Score{
				Instructions: "How severe is the reported problem?",
				Criteria: []any{
					"Cosmetic; nothing stops working",
					"A feature is broken or degraded, but a workaround exists",
					"Blocking; no workaround exists",
				},
			},
			// frustration is read on every branch: it sets the response
			// deadline whichever queue the ticket lands in.
			"frustration": typesafe.Score{
				Instructions: "How frustrated does the customer sound?",
				Criteria:     []any{"Calm and matter-of-fact", "Frustrated but civil", "Angry"},
			},
		},
	})

	// An *APIError is a refusal the server reported. Its type and request id
	// are what identifies the call in the server's own records, so they are
	// logged rather than folded into the returned error text.
	var apiErr *typesafe.APIError
	switch {
	case errors.As(err, &apiErr):
		slog.ErrorContext(ctx, "the API refused the evaluation",
			"status", apiErr.StatusCode, "type", apiErr.Type,
			"message", apiErr.Message, "request_id", apiErr.RequestID,
			"retryable", apiErr.Temporary())
		return err
	case err != nil:
		return err
	}

	category, err := resp.Answers.Choice("category")
	if err != nil {
		return err
	}
	frustration, err := resp.Answers.Score("frustration")
	if err != nil {
		return err
	}

	report := []string{
		fmt.Sprintf("ticket:      %s", ticket["subject"]),
		fmt.Sprintf("category:    %s (confidence %.2f)", category.Choice, category.Confidence),
		fmt.Sprintf("frustration: %.2f of 2", frustration.Score),
	}

	// The category is a guess until its confidence clears the bar. Below it
	// nothing downstream is read: acting on the speculative answers of a
	// branch that was picked by a coin flip is worse than not acting at all.
	if category.Confidence < autoRouteConfidence {
		report = append(report,
			fmt.Sprintf("decision:    human review (confidence below %.2f)", autoRouteConfidence))
		return writeReport(w, report)
	}

	decision, err := routeOf(resp.Answers, category.Choice)
	if err != nil {
		return err
	}
	report = append(report, "decision:    "+decision)

	// Frustration does not depend on the queue, so it is applied after the
	// branch rather than inside every arm of it.
	if frustration.Score > 1.5 {
		report = append(report, "priority:    same-day response, the customer is angry")
	}
	return writeReport(w, report)
}

// routeOf reads the answers the chosen category needs and returns the action
// to take. The answers belonging to the other categories are left unread.
func routeOf(answers typesafe.Answers, category string) (string, error) {
	switch category {
	case "billing":
		refund, err := answers.Noul("refund_requested")
		if err != nil {
			return "", err
		}
		if refund.Noul > 0.7 {
			return fmt.Sprintf("billing queue, refund flagged (p=%.2f)", refund.Noul), nil
		}
		return "billing queue", nil
	case "bug_report":
		severity, err := answers.Score("severity")
		if err != nil {
			return "", err
		}
		if severity.Score > 1.5 {
			return fmt.Sprintf("engineering, severity %.2f of 2", severity.Score), nil
		}
		return fmt.Sprintf("bug backlog, severity %.2f of 2", severity.Score), nil
	case "account":
		return "account support queue", nil
	default:
		return "product backlog", nil
	}
}

// writeReport writes the report in one call, so a failed write is reported
// once instead of being checked after every line.
func writeReport(w io.Writer, lines []string) error {
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
