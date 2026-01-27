package util

import "fmt"

// PhaseError represents an error during phase execution
type PhaseError struct {
	Phase   string
	Message string
	Err     error
}

func (e *PhaseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("phase %s failed: %s: %v", e.Phase, e.Message, e.Err)
	}
	return fmt.Sprintf("phase %s failed: %s", e.Phase, e.Message)
}

func (e *PhaseError) Unwrap() error {
	return e.Err
}

// NewPhaseError creates a new phase error
func NewPhaseError(phase, message string, err error) *PhaseError {
	return &PhaseError{
		Phase:   phase,
		Message: message,
		Err:     err,
	}
}

// RetryableError represents an error that should trigger a retry
type RetryableError struct {
	Message string
	Err     error
}

func (e *RetryableError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// NewRetryableError creates a new retryable error
func NewRetryableError(message string, err error) *RetryableError {
	return &RetryableError{
		Message: message,
		Err:     err,
	}
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	_, ok := err.(*RetryableError)
	return ok
}
