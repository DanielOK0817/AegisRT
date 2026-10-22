package contextvm

import "errors"

var (
	ErrInvalidScope             = errors.New("invalid context scope")
	ErrInvalidPage              = errors.New("invalid context page")
	ErrInvalidReference         = errors.New("invalid context reference")
	ErrPageNotFound             = errors.New("context page not found")
	ErrHashCollision            = errors.New("context page hash collision")
	ErrCounterMismatch          = errors.New("context token counter mismatch")
	ErrInvalidAccess            = errors.New("invalid access request")
	ErrAccessDenied             = errors.New("context access denied")
	ErrInvalidManifest          = errors.New("invalid context manifest")
	ErrDuplicateReference       = errors.New("duplicate context reference")
	ErrReferenceFlagsConflict   = errors.New("context reference flags conflict")
	ErrManifestRevisionOverflow = errors.New("context manifest revision overflow")
	ErrCOWSourceNotFound        = errors.New("copy-on-write source not found")
	ErrInvalidCOWPage           = errors.New("invalid copy-on-write page")
	ErrInvalidBudget            = errors.New("invalid context token budget")
	ErrPinnedBudget             = errors.New("pinned pages exceed token budget")
	ErrSummaryFailed            = errors.New("context summary failed")
	ErrSummaryBudget            = errors.New("summary exceeds token budget")
)
