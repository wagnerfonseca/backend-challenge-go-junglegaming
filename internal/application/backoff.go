package application

import (
	"encoding/binary"
	"hash/fnv"
	"time"
)

// Reference recovery bounds.
const (
	// ReferenceTTL is how long a PENDING_REFERENCE transaction waits before
	// its unresolved reference expires.
	ReferenceTTL = 24 * time.Hour
	// ReferenceBackoffBase is the delay before the first reference retry.
	ReferenceBackoffBase = time.Second
	// ReferenceBackoffCap is the largest exponential delay between retries.
	ReferenceBackoffCap = 15 * time.Minute
	// ReferenceClaimBatch is the maximum batch claimed by one worker pass.
	ReferenceClaimBatch = 50
	// ReferenceClaimLease is how long a claimed batch stays owned.
	ReferenceClaimLease = 30 * time.Second
)

// referenceJitterDenominator turns the deterministic hash into up to 10% of
// the exponential delay.
const referenceJitterDenominator = 10

// ReferenceBackoff returns the deterministic delay before the given retry
// attempt: base 1s doubled per attempt, capped at 15 minutes, shifted by a
// deterministic jitter of up to 10% derived from the transaction identity.
// The function is pure: the same attempt and identity always yield the same
// delay.
func ReferenceBackoff(attempt int, transactionID string) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := ReferenceBackoffBase
	for i := 0; i < attempt && base < ReferenceBackoffCap; i++ {
		base *= 2
		if base > ReferenceBackoffCap {
			base = ReferenceBackoffCap
		}
	}
	jitterWindow := base / referenceJitterDenominator
	if jitterWindow <= 0 {
		return base
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(transactionID))
	var attemptBytes [8]byte
	binary.BigEndian.PutUint64(attemptBytes[:], uint64(attempt))
	_, _ = hasher.Write(attemptBytes[:])
	jitter := time.Duration(hasher.Sum64() % uint64(jitterWindow+1))
	return base + jitter
}

// ReferenceNextAttemptAt returns the instant of the given retry attempt.
func ReferenceNextAttemptAt(attempt int, transactionID string, now time.Time) time.Time {
	return now.Add(ReferenceBackoff(attempt, transactionID))
}
