package pipeline

import "errors"

// ErrAmbiguous means the install outcome is unknown; callers must block the pool.
var ErrAmbiguous = errors.New("install outcome is ambiguous")

// ErrRejected means the API refused the request and nothing started.
var ErrRejected = errors.New("install request rejected")
