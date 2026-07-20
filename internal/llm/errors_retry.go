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
