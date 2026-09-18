package application_test

import (
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// C84 - Retry delay starts at 1s, doubles up to 15 minutes, and carries a
// deterministic jitter of up to 10%.
func TestReferenceBackoff(t *testing.T) {
	const identity = "018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"

	tests := []struct {
		name           string
		attempt        int
		expectedBase   time.Duration
		expectedMaxAdd time.Duration
	}{
		{name: "first attempt", attempt: 0, expectedBase: time.Second, expectedMaxAdd: 100 * time.Millisecond},
		{name: "second attempt", attempt: 1, expectedBase: 2 * time.Second, expectedMaxAdd: 200 * time.Millisecond},
		{name: "third attempt", attempt: 2, expectedBase: 4 * time.Second, expectedMaxAdd: 400 * time.Millisecond},
		{name: "capped attempt", attempt: 20, expectedBase: 15 * time.Minute, expectedMaxAdd: 90 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := application.ReferenceBackoff(tt.attempt, identity)
			if delay < tt.expectedBase {
				t.Errorf("delay = %s, want at least %s", delay, tt.expectedBase)
			}
			if delay > tt.expectedBase+tt.expectedMaxAdd {
				t.Errorf("delay = %s, want at most %s", delay, tt.expectedBase+tt.expectedMaxAdd)
			}
			if again := application.ReferenceBackoff(tt.attempt, identity); again != delay {
				t.Errorf("delay is not deterministic: %s then %s", delay, again)
			}
		})
	}

	t.Run("exponential schedule never exceeds the cap plus jitter", func(t *testing.T) {
		for attempt := 0; attempt < 12; attempt++ {
			base := time.Second
			for i := 0; i < attempt && base < 15*time.Minute; i++ {
				base *= 2
				if base > 15*time.Minute {
					base = 15 * time.Minute
				}
			}
			delay := application.ReferenceBackoff(attempt, identity)
			if delay < base || delay > base+base/10 {
				t.Errorf("attempt %d delay %s is outside [%s, %s]", attempt, delay, base, base+base/10)
			}
		}
	})

	t.Run("next attempt instant is the backoff from now", func(t *testing.T) {
		now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
		next := application.ReferenceNextAttemptAt(3, identity, now)
		delay := application.ReferenceBackoff(3, identity)
		if !next.Equal(now.Add(delay)) {
			t.Errorf("next attempt = %s, want %s", next, now.Add(delay))
		}
	})
}
