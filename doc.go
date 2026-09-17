// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

// Package typesafe is a client for the TypeSafe API
// (https://docs.typesafe.ai/api). It is unofficial and is not published by
// TypeSafe AI.
//
// One request evaluates one state against a map of typed questions and
// returns one answer per question, under the ids the request used. Three
// question types exist. [Noul] is a yes/no question answered with a
// probability, [Choice] selects one option out of a named set, and [Score]
// rates the state against an ordered rubric.
//
// # Usage
//
//	client, err := typesafe.NewClient("")  // reads TYPESAFE_API_KEY
//	if err != nil {
//		return err
//	}
//
//	resp, err := client.Evaluate(ctx, &typesafe.Request{
//		State: "Help! My payouts have been failing for 3 days.",
//		Questions: map[string]typesafe.Question{
//			"is_urgent": typesafe.Noul{
//				Instructions: "Does this convey urgency?",
//			},
//			"department": typesafe.Choice{
//				Instructions: "Which team should handle this?",
//				Criteria: map[string]any{
//					"billing":   "Payments, invoicing, refunds",
//					"technical": "Bugs, outages, integrations",
//				},
//			},
//		},
//	})
//	if err != nil {
//		return err
//	}
//
//	urgent, err := resp.Answers.Noul("is_urgent")
//	if err != nil {
//		return err
//	}
//	fmt.Println(urgent.Noul)
//
// A request the API would reject on its shape is rejected before it is sent,
// as a [*ValidationError] naming the field. A response outside 2xx is an
// [*APIError] carrying the status, the server's error type, the message and
// the request id. A client retries a request timeout, a rate limit, a
// server-side failure and a connection error on its own; see [RetryPolicy].
package typesafe
