// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

// Command function-calling turns one sentence into a call to an ordinary Go
// function.
//
// The four functions at the bottom of this file know nothing about the API.
// What connects them to a sentence is a set of questions: one Choice picks the
// function out of the four, one Choice per argument picks a value out of that
// argument's fixed set, and one Noul reports whether the sentence named a
// service at all. Because every argument's options are the values the function
// accepts, nothing has to map a label back to a Go value afterwards.
//
// Every question is sent in one request and only the chosen function's
// arguments are read. Each answer is gated on its confidence, and a judgement
// below its bar refuses the call instead of guessing: a wrong argument is a
// wrong action, and an assistant that acts on a coin flip is worse than one
// that asks.
package main

import (
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

// The bars each judgement has to clear. Picking the wrong function is the
// costlier mistake, so it carries the higher one.
const (
	functionConfidence = 0.70
	argumentConfidence = 0.60
	serviceMentioned   = 0.50
)

// request is the sentence to act on.
const request = "roll back checkout in production, the last deploy broke the cart"

// arguments holds the values the questions filled in. Every field is one of
// the option labels its question defined, so a function never sees a value it
// does not accept.
type arguments struct {
	service     string
	environment string
	logLevel    string
}

// set stores one answered argument under the question id it came back from.
func (a *arguments) set(id, value string) {
	switch id {
	case "service":
		a.service = value
	case "environment":
		a.environment = value
	case "log_level":
		a.logLevel = value
	}
}

// tool is one dispatchable function: the description that tells the Choice
// question what it covers, the argument questions to read for it, and the
// function itself.
type tool struct {
	description string
	needs       []string
	call        func(arguments) string
}

// tools is the dispatch table. The keys are the option labels of the
// "function" question, so the chosen label indexes this map directly.
var tools = map[string]tool{
	"list_deployments": {
		description: "Show what is currently deployed",
		needs:       []string{"environment"},
		call:        listDeployments,
	},
	"rollback_deployment": {
		description: "Put a service back on its previous revision",
		needs:       []string{"service", "environment"},
		call:        rollbackDeployment,
	},
	"tail_logs": {
		description: "Follow a service's log output",
		needs:       []string{"service", "environment", "log_level"},
		call:        tailLogs,
	},
	"open_incident": {
		description: "Declare an incident and page the on-call engineer",
		needs:       []string{"service"},
		call:        openIncident,
	},
}

func main() {
	if err := evaluate(context.Background(), os.Stdout); err != nil {
		slog.Error("function calling failed", "err", err)
		os.Exit(1)
	}
}

// evaluate builds the client and dispatches one request. The API key comes
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

// run maps the request onto a function and calls it, writing what it decided
// to w.
func run(ctx context.Context, client *typesafe.Client, w io.Writer) error {
	// The function options are the dispatch table's own keys, so a function
	// added below is offered here without a second edit.
	functions := make(map[string]any, len(tools))
	for name, t := range tools {
		functions[name] = t.description
	}

	resp, err := client.Evaluate(ctx, &typesafe.Request{
		State: request,
		Questions: map[string]typesafe.Question{
			"function": typesafe.Choice{
				Instructions: "What is the user asking the operations assistant to do?",
				Criteria:     functions,
			},
			"service": typesafe.Choice{
				Instructions: "Which service is the user talking about?",
				Criteria: map[string]any{
					"payments": "The service that charges cards and issues refunds",
					"checkout": "The storefront cart and checkout flow",
					"search":   "The catalogue search and indexing service",
				},
			},
			"environment": typesafe.Choice{
				Instructions: "Which environment does the user mean?",
				Criteria: map[string]any{
					"production": "The live environment customers use",
					"staging":    "The pre-release environment used for testing",
				},
			},
			"log_level": typesafe.Choice{
				Instructions: "How much log detail does the user want to see?",
				Criteria: map[string]any{
					"error": "Failures only",
					"warn":  "Failures and warnings",
					"info":  "Everything, including routine activity",
				},
			},
			// A Choice always returns its most probable option, even when the
			// sentence says nothing about a service. This reports whether one
			// was named at all, so a missing argument is a refusal rather than
			// a guess.
			"mentions_service": typesafe.Noul{
				Instructions: "The user names a specific service.",
				Criteria: &typesafe.NoulCriteria{
					True:  "A named service, product area or component appears in the request",
					False: "The request is about the system in general, or names nothing",
				},
			},
		},
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

	chosen, err := resp.Answers.Choice("function")
	if err != nil {
		return err
	}
	report := []string{
		"request:  " + request,
		fmt.Sprintf("function: %s (confidence %.2f)", chosen.Choice, chosen.Confidence),
	}
	if chosen.Confidence < functionConfidence {
		report = append(report, fmt.Sprintf(
			"refused:  which function to call is not clear enough (confidence below %.2f)",
			functionConfidence))
		return writeReport(w, report)
	}

	t, ok := tools[chosen.Choice]
	if !ok {
		return fmt.Errorf("the model chose %q, which is not in the dispatch table", chosen.Choice)
	}

	// Only the chosen function's arguments are read. The answers for the other
	// functions were evaluated in the same call and are simply ignored.
	args, lines, refusal, err := fill(resp.Answers, t.needs)
	report = append(report, lines...)
	switch {
	case err != nil:
		return err
	case refusal != "":
		report = append(report, "refused:  "+refusal)
	default:
		report = append(report, "result:   "+t.call(args))
	}
	return writeReport(w, report)
}

// fill reads the arguments one function needs. It returns the arguments, the
// report lines describing them, and a refusal when a judgement did not clear
// its bar, at which point no further argument is read.
func fill(answers typesafe.Answers, needs []string) (arguments, []string, string, error) {
	var args arguments
	var lines []string

	// A function that takes a service is not called unless the request named
	// one, whatever the service question's most probable option turned out
	// to be.
	if slices.Contains(needs, "service") {
		named, err := answers.Noul("mentions_service")
		if err != nil {
			return args, lines, "", err
		}
		if named.Noul < serviceMentioned {
			return args, lines, fmt.Sprintf(
				"the request does not name a service (p=%.2f)", named.Noul), nil
		}
	}

	for _, id := range needs {
		answer, err := answers.Choice(id)
		if err != nil {
			return args, lines, "", err
		}
		lines = append(lines, fmt.Sprintf("argument: %-11s = %-10s (confidence %.2f)",
			id, answer.Choice, answer.Confidence))
		if answer.Confidence < argumentConfidence {
			return args, lines, fmt.Sprintf(
				"the %s argument is not clear enough (confidence below %.2f)",
				id, argumentConfidence), nil
		}
		args.set(id, answer.Choice)
	}
	return args, lines, "", nil
}

// listDeployments and the three functions below it are ordinary Go functions.
// They take the arguments the questions filled in and return what the
// assistant reports back.
func listDeployments(a arguments) string {
	return "listed the running deployments in " + a.environment
}

func rollbackDeployment(a arguments) string {
	return fmt.Sprintf("rolled %s in %s back to its previous revision", a.service, a.environment)
}

func tailLogs(a arguments) string {
	return fmt.Sprintf("following %s logs for %s in %s", a.logLevel, a.service, a.environment)
}

func openIncident(a arguments) string {
	return "opened an incident for " + a.service + " and paged the on-call engineer"
}

// writeReport writes the report in one call, so a failed write is reported
// once instead of being checked after every line.
func writeReport(w io.Writer, lines []string) error {
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
