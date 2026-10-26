package qqmusic

// MobileLoginError identifies a local protocol phase without exposing provider data.
type MobileLoginError struct {
	Stage string
	Err   error
}

func (e *MobileLoginError) Error() string { return "qqmusic: mobile login failed at " + e.Stage }
func (e *MobileLoginError) Unwrap() error { return e.Err }
