// Package failpoint exposes deterministic interruption points used by the
// integration binaries. Production configuration rejects enabling them.
package failpoint

import "sync"

// Well-known failpoint names at commit, delete and publish-confirmation
// boundaries.
const (
	HTTPAfterCommit       = "http.after_commit"
	SQSAfterCommit        = "sqs.after_commit"
	SQSAfterDelete        = "sqs.after_delete"
	OutboxAfterPublish    = "outbox.after_publish"
	ReferenceAfterResolve = "reference.after_resolve"
)

// Set is the enabled failpoint registry. A nil set is inert.
type Set struct {
	mu      sync.RWMutex
	enabled map[string]struct{}
}

// New builds a failpoint set from the configured names.
func New(names []string) *Set {
	set := &Set{enabled: make(map[string]struct{}, len(names))}
	for _, name := range names {
		set.enabled[name] = struct{}{}
	}
	return set
}

// Enabled reports whether the named failpoint is active.
func (s *Set) Enabled(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.enabled[name]
	return ok
}

// Hit stops the process when the named failpoint is active, reproducing a
// crash in the window the failpoint guards. It is a no-op otherwise.
func (s *Set) Hit(name string) {
	if !s.Enabled(name) {
		return
	}
	panic("failpoint hit: " + name)
}
