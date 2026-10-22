package contextstate

import "errors"

var (
	ErrUnavailable              = errors.New("context repository unavailable")
	ErrInvalidCheckpoint        = errors.New("invalid context checkpoint")
	ErrMigrationRequired        = errors.New("context state migration required")
	ErrPreconditionFailed       = errors.New("context manifest precondition failed")
	ErrIdempotencyConflict      = errors.New("context idempotency conflict")
	ErrReferenceConflict        = errors.New("context reference conflict")
	ErrInvalidContent           = errors.New("invalid context content")
	ErrScopeForbidden           = errors.New("context visibility is forbidden")
	ErrPageTooLarge             = errors.New("context page is too large")
	ErrPageTokenLimit           = errors.New("context page token limit exceeded")
	ErrManifestPageLimit        = errors.New("context manifest page limit exceeded")
	ErrManifestTokenLimit       = errors.New("context manifest token limit exceeded")
	ErrPinnedTokenLimit         = errors.New("context pinned token limit exceeded")
	ErrCheckpointCapacity       = errors.New("context checkpoint capacity exceeded")
	ErrTaskContextDisabled      = errors.New("task context budget is disabled")
	ErrInvalidTaskBinding       = errors.New("invalid context task binding")
	ErrInvalidFanout            = errors.New("invalid context fanout")
	ErrInvalidIdempotencyKey    = errors.New("invalid context idempotency key")
	ErrInvalidOutputPublication = errors.New("invalid context output publication")
	ErrOutputNotFound           = errors.New("context output not found")
	ErrOutputCapacity           = errors.New("context output capacity exceeded")
	ErrSummaryCapacity          = errors.New("context summary cache capacity exceeded")
)
