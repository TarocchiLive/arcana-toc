package qqmusic

import "errors"

// Close cancels pending enrollments, waits for their bounded I/O, and releases
// the credential transaction lock.
func (a *Adapter) Close() error {
	a.loginMu.Lock()
	if a.closed {
		a.loginMu.Unlock()
		return nil
	}
	a.closed = true
	enrollment := a.enrollment
	a.enrollmentStarting = false
	a.enrollment = nil
	lock := a.enrollmentLock
	a.enrollmentLock = nil
	if enrollment != nil {
		enrollment.cancel()
	}
	a.loginMu.Unlock()

	a.mu.Lock()
	sessions := a.mobile
	a.mobile = nil
	for _, session := range sessions {
		session.cancel()
	}
	a.mu.Unlock()
	if enrollment != nil {
		<-enrollment.done
	}
	for _, session := range sessions {
		<-session.done
	}
	if a.client != nil {
		a.client.CloseIdleConnections()
	}
	if lock != nil {
		return errors.Join(lock.Close())
	}
	return nil
}
