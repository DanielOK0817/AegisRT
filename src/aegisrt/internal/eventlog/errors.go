package eventlog

import "errors"

var (
	ErrInvalidCapacity   = errors.New("invalid event log capacity")
	ErrInvalidEvent      = errors.New("invalid runtime event")
	ErrInvalidIdentifier = errors.New("invalid event identifier")
	ErrInvalidPayload    = errors.New("invalid event JSON payload")
	ErrPayloadTooLarge   = errors.New("event payload exceeds size limit")
	ErrEventIDAssigned   = errors.New("event ID must be assigned by log")
	ErrIDExhausted       = errors.New("event ID space exhausted")
	ErrInvalidLimit      = errors.New("invalid replay limit")
	ErrInvalidStream     = errors.New("invalid event stream identity")
)
