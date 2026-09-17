# Changelog

Every tag has a section here, and the section is the body of the GitHub
release. A tag without one fails the release workflow. Write under
`Unreleased` as work lands; before tagging, turn that into
`## vX.Y.Z - YYYY-MM-DD` in the same commit the tag points at.

A section says what changed for whoever uses the release, not what was
committed: the commit log already holds that.

## Unreleased

First working client. It speaks the TypeSafe evaluation API from Go with no
dependencies beyond the standard library.

- `Client.Evaluate` sends one state and a map of typed questions and returns
  one answer per question. Questions are `Noul` (yes/no probability), `Choice`
  (one option out of a named set) and `Score` (a position on an ordered
  rubric). A state, an instruction and a criterion description can each be a
  string, a map or a slice, so structured content needs no flattening.
- Answers are read through `Answers.Noul`, `Answers.Choice` and
  `Answers.Score`, which name the id and the type that was found when a lookup
  does not fit. `ScoreAnswer.Levels` puts the legend back in rubric order. An
  answer type the client does not model arrives as `UnknownAnswer` with its raw
  JSON rather than failing the response.
- `Client.ListModels` returns the models and aliases the account may select,
  with the release date as a `time.Time`.
- A request the API would reject on its shape is rejected before it is sent, as
  a `*ValidationError` naming the field. A response outside 2xx is an
  `*APIError` carrying the status, the server's error type, the message, the
  request id and the raw body, for every error shape the API uses. A connection
  failure or a timeout is wrapped, so `errors.Is` reaches
  `context.DeadlineExceeded` and `errors.As` reaches `net.Error`.
- Request timeouts, rate limits, server-side failures and connection errors are
  retried with exponential backoff and jitter, honouring `retry-after-ms` and
  `Retry-After` up to a cap. Waiting respects the caller's context, and the
  request body is replayed on every attempt. `RetryPolicy` and the per-attempt
  timeout are configurable; the zero `RetryPolicy` turns retries off.
- `examples/` holds four runnable programs, each carrying its own input data:
  `ticket-triage` routes a support ticket from one request and gates the branch
  on confidence, `composite-scoring` combines four rubrics into one weighted
  number, `rerank` reorders a search shortlist with one question per candidate,
  and `function-calling` maps a sentence onto a Go function and its arguments,
  refusing to call anything when a judgement is unclear. Run one with
  `TYPESAFE_API_KEY=... go run ./examples/<name>`.
- The API key, the API root and the default model come from `TYPESAFE_API_KEY`,
  `TYPESAFE_BASE_URL` and `TYPESAFE_DEFAULT_MODEL`, each overridable with an
  option. A client is safe for concurrent use.
