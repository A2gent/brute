package llm

import "errors"

type unsafeForRetryError struct {
	err error
}

func (e *unsafeForRetryError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *unsafeForRetryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// UnsafeForRetry marks an error as unsafe to retry while preserving its message.
func UnsafeForRetry(err error) error {
	if err == nil {
		return nil
	}
	return &unsafeForRetryError{err: err}
}

// IsUnsafeForRetry reports whether err was marked unsafe for retry.
func IsUnsafeForRetry(err error) bool {
	var marker *unsafeForRetryError
	return errors.As(err, &marker)
}

// nonRetryableError forbids repeating a request, but unlike unsafeForRetryError
// does not imply side effects that would make switching providers unsafe.
type nonRetryableError struct{ error }

func (e *nonRetryableError) Unwrap() error { return e.error }

// NonRetryable preserves an error while forbidding retries on the same provider.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &nonRetryableError{err}
}

// IsNonRetryable reports whether same-provider retries were explicitly forbidden.
func IsNonRetryable(err error) bool {
	var marker *nonRetryableError
	return errors.As(err, &marker)
}
