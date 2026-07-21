package remnawave

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// RetryWithBackoff retries fn on retryable API errors up to max attempts.
func RetryWithBackoff(ctx context.Context, maxAttempts int, fn func() error) error {
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		last = err
		var apiErr *APIError
		ok := errors.As(err, &apiErr)
		if !ok || !apiErr.IsRetryable() {
			return err
		}
		// Exponential backoff with jitter: 0.5s, 1s, 2s, 4s, ...
		backoff := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
		if backoff > 8*time.Second {
			backoff = 8 * time.Second
		}
		backoff += time.Duration(rand.IntN(250)) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return last
}
