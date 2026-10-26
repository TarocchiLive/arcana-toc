package qqmusic

import "fmt"

// CredentialRefreshError retains the provider result code without retaining its
// potentially sensitive response, matching Python's refresh-specific failure.
type CredentialRefreshError struct {
	Code int
}

func (e *CredentialRefreshError) Error() string {
	return fmt.Sprintf("qqmusic: credential refresh failed (%d)", e.Code)
}

func (e *CredentialRefreshError) Unwrap() error { return resultError(e.Code) }
