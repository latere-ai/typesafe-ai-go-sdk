# typesafe-ai-go-sdk

[![CI](https://github.com/latere-ai/typesafe-ai-go-sdk/actions/workflows/ci.yml/badge.svg)](https://github.com/latere-ai/typesafe-ai-go-sdk/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/latere-ai/typesafe-ai-go-sdk.svg)](https://pkg.go.dev/github.com/latere-ai/typesafe-ai-go-sdk)
[![Release](https://img.shields.io/github/v/release/latere-ai/typesafe-ai-go-sdk)](https://github.com/latere-ai/typesafe-ai-go-sdk/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/latere-ai/typesafe-ai-go-sdk)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Go client for the [TypeSafe API](https://docs.typesafe.ai/api). Unofficial, and
not published by TypeSafe AI.

The API evaluates content against typed questions. A question is one of three
types: `noul` returns a yes/no probability, `choice` selects one option from a
set, and `score` rates content along a rubric.

## Install

```sh
go get github.com/latere-ai/typesafe-ai-go-sdk
```

The package imports the standard library only.

## Use it

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/latere-ai/typesafe-ai-go-sdk"
)

func main() {
	client, err := typesafe.NewClient("") // reads TYPESAFE_API_KEY
	if err != nil {
		slog.Error("new client", "err", err)
		return
	}

	resp, err := client.Evaluate(context.Background(), &typesafe.Request{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True:  "Explicitly time-sensitive",
					False: "No urgency expressed",
				},
			},
			"department": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: map[string]any{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts",
				},
			},
			"frustration": typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     []any{"Calm", "Frustrated", "Very angry"},
			},
		},
	})

	var apiErr *typesafe.APIError
	switch {
	case errors.As(err, &apiErr):
		slog.Error("the API refused the request",
			"status", apiErr.StatusCode, "type", apiErr.Type,
			"message", apiErr.Message, "request_id", apiErr.RequestID)
		return
	case err != nil:
		slog.Error("evaluate", "err", err)
		return
	}

	urgent, err := resp.Answers.Noul("is_urgent")
	if err != nil {
		slog.Error("reading is_urgent", "err", err)
		return
	}
	department, err := resp.Answers.Choice("department")
	if err != nil {
		slog.Error("reading department", "err", err)
		return
	}
	frustration, err := resp.Answers.Score("frustration")
	if err != nil {
		slog.Error("reading frustration", "err", err)
		return
	}

	fmt.Println(urgent.Noul)                // 0.95
	fmt.Println(department.Choice)          // billing
	fmt.Println(department.Confidence)      // 0.94
	fmt.Println(frustration.Score)          // 1.03 on a 0 to 2 rubric
	fmt.Println(resp.Model, resp.RequestID) // jev-1.13.0 req_...
	fmt.Println(*resp.Usage.InputTokens)    // 378
}
```

`State` takes a string, a map or a slice, so a chat log or a record can be
evaluated without flattening it to text first. The same goes for
`Instructions` and for every criterion description.

## Examples

Four runnable programs under [`examples/`](examples), each with its own input
data. Run one with `TYPESAFE_API_KEY=... go run ./examples/<name>`.

| Example | What it does |
| --- | --- |
| [`ticket-triage`](examples/ticket-triage) | Routes a support ticket from one request, gating the branch on the category's confidence. |
| [`composite-scoring`](examples/composite-scoring) | Rates a pull request description as four `Score` questions, normalised and weighted in Go. |
| [`rerank`](examples/rerank) | Reorders a search shortlist by asking one `Noul` per candidate in a single request. |
| [`function-calling`](examples/function-calling) | Maps a sentence onto a Go function and its closed-set arguments, refusing when confidence is low. |

## Answers

`resp.Answers` is a map keyed by the question ids the request used. Read it
through the typed accessors, which fail with a descriptive error when an id is
missing or holds another type:

| Accessor | Returns | Fields |
| --- | --- | --- |
| `Answers.Noul(id)` | `NoulAnswer` | `Noul` |
| `Answers.Choice(id)` | `ChoiceAnswer` | `Choice`, `Probabilities`, `Confidence` |
| `Answers.Score(id)` | `ScoreAnswer` | `Score`, `Legend`, `Probabilities`, `Confidence` |

`ScoreAnswer.Levels()` returns the legend in rubric order. An answer of a type
this package does not model arrives as `UnknownAnswer` with its raw JSON, so a
new server-side answer type does not break a response.

`Usage.InputTokens` and `Usage.OutputTokens` are pointers: nil is a count the
server did not report, which a zero would otherwise hide.

## Errors

- `*ValidationError` is a request rejected before it was sent. `Field` names the
  offending part of the request, such as `questions[frustration].Criteria`.
- `*APIError` is a response outside 2xx. It carries `StatusCode`, the server's
  `Type`, the `Message`, the `RequestID` and the raw `Body`.
  `(*APIError).Temporary()` reports whether sending the request again may work.
- A connection failure or a timeout is returned wrapped, so
  `errors.Is(err, context.DeadlineExceeded)` and `errors.As(err, &netErr)`
  both reach the cause.

## Retries and timeouts

A request timeout, a rate limit, a server-side failure and a connection error
are retried on their own. The wait before retry `n` is

```
delay_n = min(MaxBackoff, InitialBackoff * 2^n) * (1 - U(0, Jitter))
```

A `Retry-After` or `retry-after-ms` header replaces that wait unless it asks
for longer than `MaxRetryAfter`. Waiting always respects the caller's context.

```go
client, err := typesafe.NewClient("",
	typesafe.WithTimeout(30*time.Second), // per attempt; default 10s
	typesafe.WithRetryPolicy(typesafe.RetryPolicy{
		MaxRetries:     4,                // default 2
		InitialBackoff: time.Second,      // default 500ms
		MaxBackoff:     20 * time.Second, // default 5s
		Jitter:         0.3,              // default 0.25
		MaxRetryAfter:  30 * time.Second, // default 60s
	}),
	typesafe.WithLogger(slog.Default()), // one debug record per retry
)
```

The zero `RetryPolicy` turns retries off. Only `MaxRetries` means anything at
zero; every other field left at zero takes its default, so
`RetryPolicy{MaxRetries: 5}` is five retries on the default schedule.

Other options: `WithBaseURL`, `WithHTTPClient`, `WithDefaultModel`,
`WithUserAgent`. A client is safe for concurrent use.

## Models

```go
models, err := client.ListModels(ctx)
if err != nil {
	return err
}
for _, m := range models {
	fmt.Println(m.Name, m.Description, m.ReleaseDate)
}
```

`ReleaseDate` is a `time.Time`.

## Environment

| Variable | Sets | Default |
| --- | --- | --- |
| `TYPESAFE_API_KEY` | the API key, when none is passed to `NewClient` | none; required |
| `TYPESAFE_BASE_URL` | the API root | `https://api.typesafe.ai` |
| `TYPESAFE_DEFAULT_MODEL` | the model for a request that leaves `Model` empty | `jev-latest` |

A whitespace-only value counts as unset. An option always wins over a variable.

## License

Apache-2.0
