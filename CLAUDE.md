# sagas

## Rules

- The core module imports only the standard library.
- Positional arguments of `New` are a name and a flow. A step is a type that embeds `sagas.Step[S, R]` or, for the pivot, `sagas.Pivot[S, R]`. Saga settings are functional options.
- `docs/glossary.md` is the vocabulary.
- Take the simplest code that meets each rule. Measure performance with a benchmark
  instead of guessing, and record the numbers when a change touches a hot path.
- Tests prove each rule through the real code path of each row. No test doubles of our own
  code; a test may use a real standard-library implementation, such as `slog.NewJSONHandler`.
- An include anchor (`// --8<-- [start:name]` and `// --8<-- [end:name]`) marks a snippet that a docs page
  shows alone. Every name the snippet uses is defined in that snippet, in another snippet of the same program,
  in the standard library or in package `sagas`. Anchor pairs never nest, and no test-only code sits inside one.

## API style

- A step's behavior is its methods. Middleware is a value with `Wrap(ctx, info, call)`.
- A port interface has the fewest methods its callers need, one when possible. A new
  capability is a new small interface, not a new method on an existing one.
- Middleware gets the call data as the `CallInfo` parameter of `Wrap`. A step method reads the idempotency key with `IdempotencyKey(ctx)`.
  Middleware never receives the saga state.
- A concern belongs in middleware when it acts on one call, needs neither the graph nor the
  state, and varies per application. Everything else stays in the core, which is itself a
  fixed pipeline of private middlewares in one documented order.
- Reason: small interfaces and composable functions let any transport, store, or telemetry
  plug in without the core knowing about it, and keep the public surface easy to learn.
