// Package containerlog exists only to hold tests that need a *container-backed* logger
// factory installed.
//
// It is a separate package for two reasons, both structural:
//
//   - log.SetLoggerFactory panics on a second call, and the `service` package's one slot
//     is already claimed by container_warning_test.go's capturing factory.
//   - `service` cannot import `provider` — provider/app_provider.go imports service — so
//     the real provider.LoggerProvider cannot be used from a service test. The factory
//     here is hand-rolled to the same shape.
//
// The shape is what matters. container_warning_test.go installs a factory that returns a
// logger *directly*, so it exercises the rebind-warning line and passes. Swap in a factory
// that resolves core.Logger back out of the container — which is exactly what
// LoggerProvider installs — and the same line used to deadlock the process
// unconditionally. That one-line difference is why the bug shipped past a green suite.
package containerlog
