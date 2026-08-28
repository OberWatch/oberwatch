package alert

import "time"

// Clock supplies time to the dispatcher so backoff and rate limiting can be
// driven deterministically in tests.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives after d has elapsed.
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

// Now returns the wall-clock time.
func (realClock) Now() time.Time {
	return time.Now()
}

// After waits for d using the wall clock.
func (realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}
