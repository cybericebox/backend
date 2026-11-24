package assistant

// ResendSkipped tells whether a resend of the request was skipped while it is being handled.
func (u *UseCase) ResendSkipped(id string) bool {
	u.outbox.mu.Lock()
	defer u.outbox.mu.Unlock()
	f := u.outbox.inflight[id]
	return f != nil && f.resentOn != nil
}

// AfterAnswer sets a function that runs after the answer of an outbox request was sent and before its slot is freed.
func (u *UseCase) AfterAnswer(fn func(requestID string)) { u.afterAnswer = fn }
