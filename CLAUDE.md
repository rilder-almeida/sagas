# sagas

## Rules

- The core module imports only the standard library.
- Positional arguments of `New` are a name and a flow. A step is a type that embeds `sagas.Step`. Saga and run settings are functional options.
- `docs/glossary.md` is the vocabulary.
- Take the simplest code that meets each rule. Measure performance with a benchmark
  instead of guessing, and record the numbers when a change touches a hot path.
- Tests prove each rule through the real code path of each row. No test doubles of our own
  code; a test may use a real standard-library implementation, such as `slog.NewJSONHandler`.

## API style

- A step's behavior is its methods. Middleware is a value with `Wrap(ctx, info, call)`. A port is an interface with the fewest methods its callers need.
- A port interface has the fewest methods its callers need, one when possible. A new
  capability is a new small interface, not a new method on an existing one.
- Middleware gets the call data as the `CallInfo` parameter of `Wrap`. A step method reads the idempotency key with `IdempotencyKey(ctx)`.
  Middleware never receives the saga state.
- A concern belongs in middleware when it acts on one call, needs neither the graph nor the
  state, and varies per application. Everything else stays in the core, which is itself a
  fixed pipeline of private middlewares in one documented order.
- Reason: small interfaces and composable functions let any transport, store, or telemetry
  plug in without the core knowing about it, and keep the public surface easy to learn.
