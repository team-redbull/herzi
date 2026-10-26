package pipeline

import "errors"

// ErrAmbiguous marks an Install whose outcome is unknown (timeout, connection error, 5xx).
var ErrAmbiguous = errors.New("install outcome is ambiguous")

// ErrRejected marks a request the API refused (4xx, e.g. 422): our bug, nothing started.
var ErrRejected = errors.New("install request rejected")
