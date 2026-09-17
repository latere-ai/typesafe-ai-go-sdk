# Examples

Four runnable programs, each one use case. Every program carries its own input
data, so nothing needs to be downloaded and no flags are required.

```sh
TYPESAFE_API_KEY=... go run ./examples/<name>
```

`TYPESAFE_BASE_URL` points a program at another API root, and
`TYPESAFE_DEFAULT_MODEL` selects the model. The sample outputs below are the
ones the tests pin, so the numbers are canned rather than live; a real run
returns whatever the model answers.

## ticket-triage

Routes one support ticket. Every question the decision tree can reach goes out
in a single request, including the ones that only matter for some categories,
and the code reads only the answers its branch needs. The category is then
gated on its confidence: a confident classification is acted on, an unconfident
one goes to a human.

Shows: several question types in one request, a structured state, confidence
gating, and `*typesafe.APIError` handling.

```sh
TYPESAFE_API_KEY=... go run ./examples/ticket-triage
```

```
ticket:      Charged twice for order #98423, and now I cannot log in
category:    billing (confidence 0.94)
frustration: 1.70 of 2
decision:    billing queue, refund flagged (p=0.88)
priority:    same-day response, the customer is angry
```

## composite-scoring

Rates a pull request description by breaking one judgement into four
independent `Score` questions, each with its own rubric. Rubrics differ in
length, so every answer is normalised to 0 to 1 by its own top level before the
weights defined in Go combine them into one number.

Shows: several `Score` questions in one request, normalising by rubric length,
and weighting in code rather than in the prompt.

```sh
TYPESAFE_API_KEY=... go run ./examples/composite-scoring
```

```
pull request: Add a retry budget to the payments client

dimension             raw    of  normalised  weight  contribution
problem stated       3.10     4       0.775    0.30         0.232
change described     2.40     3       0.800    0.25         0.200
test evidence        3.60     4       0.900    0.30         0.270
risk and rollback    0.80     2       0.400    0.15         0.060

weighted total: 0.762 of 1.000
verdict:        ready for review
```

## rerank

Reorders a keyword-search shortlist by relevance. The query and all eight
candidate passages go into one state, and each question names one candidate by
its id, so the answers come back under those same ids and pair with the
shortlist without any bookkeeping. The score is a `Noul`: a yes/no question
answered with a probability is already a continuous ranking signal, and it
needs no rubric.

Shows: many questions in one request, answers keyed by caller-chosen ids, and
`Noul` used as a score.

```sh
TYPESAFE_API_KEY=... go run ./examples/rerank
```

```
query: How do I rotate the production database password without downtime?

rank  was  relevance  id       passage
   1    3       0.94  cand_3   Rotate a credential by adding the new password as a second ...
   2    6       0.86  cand_6   The connection pool reloads its credentials when the mounte...
   3    1       0.31  cand_1   Database passwords are stored in the secret manager under t...
   4    4       0.22  cand_4   The password policy requires 24 characters, rotation every ...
   5    2       0.12  cand_2   To restart the production database, drain the connection po...
   6    7       0.11  cand_7   Production access requires a break-glass ticket. The ticket...
   7    5       0.08  cand_5   Downtime during a deploy usually comes from a rollout that ...
   8    8       0.05  cand_8   Staging databases are reset nightly, so a password set ther...

top hit moved up from position 3
```

## function-calling

Turns one sentence into a call to an ordinary Go function. A `Choice` picks the
function out of a dispatch table, one `Choice` per argument picks a value out
of that argument's fixed set, and a `Noul` reports whether the sentence named a
service at all. Every judgement is gated on its confidence, and one below its
bar refuses the call instead of guessing.

Shows: `Choice` over a closed set that maps straight onto Go values, a `Noul`
that distinguishes "not stated" from "most probable option", and per-argument
confidence gating.

```sh
TYPESAFE_API_KEY=... go run ./examples/function-calling
```

```
request:  roll back checkout in production, the last deploy broke the cart
function: rollback_deployment (confidence 0.93)
argument: service     = checkout   (confidence 0.97)
argument: environment = production (confidence 0.99)
result:   rolled checkout in production back to its previous revision
```

When a judgement does not clear its bar, nothing is called:

```
request:  roll back checkout in production, the last deploy broke the cart
function: rollback_deployment (confidence 0.44)
refused:  which function to call is not clear enough (confidence below 0.70)
```
