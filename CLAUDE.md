# sagas

## Rules

- The core module imports only the standard library.
- Positional arguments are a name and an action. Everything else is a functional option.
- `docs/glossary.md` is the vocabulary.
- Take the simplest code that meets each rule. Measure performance with a benchmark
  instead of guessing, and record the numbers when a change touches a hot path.
- Tests prove each rule through the real code path of each row. No test doubles of our own
  code; a test may use a real standard-library implementation, such as `slog.NewJSONHandler`.

## API style

- Extension points are function types, not interfaces: a handler is a function, and
  middleware is a function that wraps a handler (`func(next Handler) Handler`).
- A port interface has the fewest methods its callers need, one when possible. A new
  capability is a new small interface, not a new method on an existing one.
- Per-call metadata travels in the `context.Context` and is read through exported helpers.
  Middleware never receives the saga state.
- A concern belongs in middleware when it acts on one call, needs neither the graph nor the
  state, and varies per application. Everything else stays in the core, which is itself a
  fixed pipeline of private middlewares in one documented order.
- Reason: small interfaces and composable functions let any transport, store, or telemetry
  plug in without the core knowing about it, and keep the public surface easy to learn.
