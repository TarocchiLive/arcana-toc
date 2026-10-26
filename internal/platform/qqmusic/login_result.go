package qqmusic

import "errors"

func (a *Adapter) saveLoginResponse(response CGIResponse) error {
	a.loginMu.Lock()
	enrollment := a.enrollment
	expired := enrollment != nil && enrollment.ctx.Err() != nil
	a.loginMu.Unlock()
	if expired {
		return errors.New("qqmusic: expired QR session")
	}
	if response.Code != 0 {
		return resultError(response.Code)
	}
	if code := intValue(response.Data["code"]); code != 0 {
		return resultError(code)
	}
	var credential Credential
	if err := mapToCredential(response.Data, &credential); err != nil {
		return errors.New("qqmusic: invalid login credential")
	}
	if err := ValidateCredential(credential); err != nil {
		return err
	}
	if a.credentialPath != "" {
		a.loginMu.Lock()
		if a.enrollmentLock != nil {
			if err := WriteCredential(a.credentialPath, credential); err != nil {
				a.loginMu.Unlock()
				return err
			}
			a.loginMu.Unlock()
		} else {
			lock, err := LockCredential(a.credentialPath)
			if err != nil {
				a.loginMu.Unlock()
				return err
			}
			a.loginMu.Unlock()
			writeErr := WriteCredential(a.credentialPath, credential)
			closeErr := lock.Close()
			if writeErr != nil || closeErr != nil {
				return errors.Join(writeErr, closeErr)
			}
		}
	}
	a.mu.Lock()
	a.credential = credential
	a.mu.Unlock()
	return nil
}
