package schema

import "errors"

var (
	// ErrNoMatch means no local pre-filter rule matched.
	ErrNoMatch = errors.New("schema: no match")
	// ErrInvalidDecision means a decision failed validation or could not be parsed.
	ErrInvalidDecision = errors.New("schema: invalid decision")
	// ErrClassifierUnavailable means the classifier could not be reached or refused.
	ErrClassifierUnavailable = errors.New("schema: classifier unavailable")
	// ErrRunFailed means a headless run exited non-zero.
	ErrRunFailed = errors.New("schema: run failed")
	// ErrTimeout means an operation exceeded its deadline.
	ErrTimeout = errors.New("schema: timeout")
)
