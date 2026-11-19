package livesocket

import (
	"testing"
	"time"
)

func TestMinuteLimiter(t *testing.T) {
	now := time.Unix(1_000, 0)
	l := NewMinuteLimiter(2)
	l.now = func() time.Time { return now }
	l.start = now

	for range 2 {
		if !l.Allow() {
			t.Fatal("refused within the limit")
		}
	}
	if l.Allow() {
		t.Fatal("allowed over the limit")
	}
	now = now.Add(59 * time.Second)
	if l.Allow() {
		t.Fatal("window reset early")
	}
	now = now.Add(time.Second)
	if !l.Allow() {
		t.Fatal("window did not reset after a minute")
	}

	unlimited := NewMinuteLimiter(-1)
	for range 1_000 {
		if !unlimited.Allow() {
			t.Fatal("-1 limited")
		}
	}
}
