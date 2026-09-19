package clock

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRealNowIsUTCAndCurrent(t *testing.T) {
	before := time.Now()
	got := Real{}.Now()
	after := time.Now()

	if got.Location() != time.UTC {
		t.Errorf("Real.Now() location = %v, want UTC", got.Location())
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("Real.Now() = %v, not between %v and %v", got, before, after)
	}
}

func TestFakeNewFakeNormalisesToUTC(t *testing.T) {
	zone := time.FixedZone("WIB", 7*3600)
	in := time.Date(2026, 9, 19, 15, 30, 0, 0, zone)

	f := NewFake(in)

	got := f.Now()
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
	if !got.Equal(in) {
		t.Errorf("Now() = %v, want the same instant as %v", got, in)
	}
	if want := time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
}

func TestFakeSetAndAdvance(t *testing.T) {
	start := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	f := NewFake(start)

	f.Advance(90 * time.Minute)
	if want := start.Add(90 * time.Minute); !f.Now().Equal(want) {
		t.Errorf("after Advance: Now() = %v, want %v", f.Now(), want)
	}

	f.Advance(-30 * time.Minute)
	if want := start.Add(60 * time.Minute); !f.Now().Equal(want) {
		t.Errorf("after negative Advance: Now() = %v, want %v", f.Now(), want)
	}

	other := time.Date(2030, 1, 2, 3, 4, 5, 6, time.FixedZone("X", -5*3600))
	f.Set(other)
	if !f.Now().Equal(other) {
		t.Errorf("after Set: Now() = %v, want %v", f.Now(), other)
	}
	if f.Now().Location() != time.UTC {
		t.Errorf("after Set: location = %v, want UTC", f.Now().Location())
	}
}

func TestFakeNowIsStableWithoutAdvance(t *testing.T) {
	f := NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if a, b := f.Now(), f.Now(); !a.Equal(b) {
		t.Errorf("Now() changed without Advance: %v vs %v", a, b)
	}
}

// Run with -race: concurrent readers and writers must not race.
func TestFakeConcurrentUse(t *testing.T) {
	f := NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	start := f.Now()

	const workers, iterations = 8, 200
	var wg sync.WaitGroup
	for range workers {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for range iterations {
				f.Advance(time.Second)
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				_ = f.Now()
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				f.Set(f.Now())
			}
		}()
	}
	wg.Wait()

	// Set(Now()) may overwrite concurrent Advances, so only bound the result.
	if got := f.Now(); got.Before(start) || got.After(start.Add(workers*iterations*time.Second)) {
		t.Errorf("Now() = %v outside [%v, %v]", got, start, start.Add(workers*iterations*time.Second))
	}
}

func TestTruncateMicro(t *testing.T) {
	loc := time.FixedZone("X", 3600)
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			name: "drops nanoseconds below a microsecond",
			in:   time.Date(2026, 9, 19, 8, 30, 0, 123_456_789, time.UTC),
			want: time.Date(2026, 9, 19, 8, 30, 0, 123_456_000, time.UTC),
		},
		{
			name: "already at microsecond precision",
			in:   time.Date(2026, 9, 19, 8, 30, 0, 123_456_000, time.UTC),
			want: time.Date(2026, 9, 19, 8, 30, 0, 123_456_000, time.UTC),
		},
		{
			name: "truncates rather than rounds up",
			in:   time.Date(2026, 9, 19, 8, 30, 0, 999_999_999, time.UTC),
			want: time.Date(2026, 9, 19, 8, 30, 0, 999_999_000, time.UTC),
		},
		{
			name: "whole seconds unchanged",
			in:   time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC),
			want: time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC),
		},
		{
			name: "keeps the location",
			in:   time.Date(2026, 9, 19, 8, 30, 0, 1_999, loc),
			want: time.Date(2026, 9, 19, 8, 30, 0, 1_000, loc),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateMicro(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("TruncateMicro(%v) = %v, want %v", tc.in, got, tc.want)
			}
			if got.Location() != tc.in.Location() {
				t.Errorf("location = %v, want %v", got.Location(), tc.in.Location())
			}
		})
	}
}

func TestTruncateMicroStripsMonotonicReading(t *testing.T) {
	now := time.Now()
	if !strings.Contains(now.String(), "m=") {
		t.Skip("time.Now() carries no monotonic reading on this platform")
	}
	// A time with a monotonic reading prints "m=+..."; Truncate removes it.
	if s := TruncateMicro(now).String(); strings.Contains(s, "m=") {
		t.Errorf("TruncateMicro kept a monotonic reading: %s", s)
	}
}
