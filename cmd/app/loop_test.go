package main

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	var b backoff
	var got []time.Duration
	for range 8 {
		got = append(got, b.next())
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("step %d: got %s, want %s", i, got[i], want[i]*time.Second)
		}
	}

	b.reset()
	if d := b.next(); d != minBackoff {
		t.Errorf("after reset: got %s, want %s", d, minBackoff)
	}
}
