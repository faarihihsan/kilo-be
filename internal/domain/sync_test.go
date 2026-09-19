package domain

import (
	"errors"
	"testing"
	"time"

	"workout-tracker-be/internal/clock"
)

func TestDecideSyncDecisionTable(t *testing.T) {
	stored := time.Date(2026, 9, 19, 8, 55, 2, 123456000, time.UTC) // exactly 123456 µs
	live := &SyncState{ClientUpdatedAt: stored}
	deleted := &SyncState{ClientUpdatedAt: stored, Deleted: true}

	tests := []struct {
		name     string
		existing *SyncState
		incoming time.Time
		want     SyncAction
	}{
		// spec 03, conflict rule table, one case per row.
		{"no row -> insert", nil, stored, SyncInsert},
		{"no row, zero incoming -> insert", nil, time.Time{}, SyncInsert},
		{"deleted, incoming older -> deleted", deleted, stored.Add(-time.Hour), SyncDeleted},
		{"deleted, incoming equal -> deleted", deleted, stored, SyncDeleted},
		{"deleted, incoming newer -> deleted (deleted wins)", deleted, stored.Add(time.Hour), SyncDeleted},
		{"older -> stale", live, stored.Add(-time.Second), SyncStale},
		{"older by 1 µs -> stale", live, stored.Add(-time.Microsecond), SyncStale},
		{"equal -> noop", live, stored, SyncNoop},
		{"newer by 1 µs -> update", live, stored.Add(time.Microsecond), SyncUpdate},
		{"newer -> update", live, stored.Add(time.Second), SyncUpdate},

		// Microsecond precision: differences below 1 µs must compare equal.
		{"newer by 1 ns is the same µs -> noop", live, stored.Add(time.Nanosecond), SyncNoop},
		{"newer by 999 ns is the same µs -> noop", live, stored.Add(999 * time.Nanosecond), SyncNoop},
		{"older by 1 ns crosses to the previous µs -> stale", live, stored.Add(-time.Nanosecond), SyncStale},
		{"older by 999 ns -> stale", live, stored.Add(-999 * time.Nanosecond), SyncStale},
		{"newer by 1000 ns -> update", live, stored.Add(1000 * time.Nanosecond), SyncUpdate},

		// Instants, not wall clocks: the zone of either side is irrelevant.
		{"same instant, other zone -> noop", live, stored.In(time.FixedZone("UTC+7", 7*3600)), SyncNoop},
		{"older instant, other zone -> stale", live, stored.Add(-time.Minute).In(time.FixedZone("UTC-5", -5*3600)), SyncStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideSync(tc.existing, tc.incoming); got != tc.want {
				t.Fatalf("DecideSync = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecideSyncSubMicrosecondStoredValue(t *testing.T) {
	// The stored value normally comes back from Postgres at µs precision, but
	// the rule must not depend on that: nanoseconds on either side are dropped.
	base := time.Date(2026, 9, 19, 8, 0, 0, 500_000, time.UTC) // .000500 s
	withNS := &SyncState{ClientUpdatedAt: base.Add(789 * time.Nanosecond)}
	if got := DecideSync(withNS, base); got != SyncNoop {
		t.Fatalf("stored has ns, incoming exact µs: got %v, want noop", got)
	}
	if got := DecideSync(withNS, base.Add(999*time.Nanosecond)); got != SyncNoop {
		t.Fatalf("both in the same µs: got %v, want noop", got)
	}
	if got := DecideSync(withNS, base.Add(-time.Nanosecond)); got != SyncStale {
		t.Fatalf("incoming in the previous µs: got %v, want stale", got)
	}
}

func TestDecideSyncPreEpochAndExtremeTimes(t *testing.T) {
	// Flooring must work before 1970 and at the ends of the range.
	pre := time.Unix(-1, 999_999_999).UTC() // 1969-12-31T23:59:59.999999999Z
	same := &SyncState{ClientUpdatedAt: time.Unix(-1, 999_999_000).UTC()}
	if got := DecideSync(same, pre); got != SyncNoop {
		t.Fatalf("pre-epoch same µs: got %v, want noop", got)
	}
	zero := &SyncState{ClientUpdatedAt: time.Time{}}
	if got := DecideSync(zero, time.Time{}); got != SyncNoop {
		t.Fatalf("zero time equal: got %v, want noop", got)
	}
	if got := DecideSync(zero, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)); got != SyncUpdate {
		t.Fatalf("far future: got %v, want update", got)
	}
}

func TestDecideSyncIgnoresMonotonicReading(t *testing.T) {
	// time.Now carries a monotonic reading; a copy without it is the same
	// instant and must not be treated as different.
	now := time.Now()
	wall := time.Unix(0, now.UnixNano()).UTC() // no monotonic reading
	if got := DecideSync(&SyncState{ClientUpdatedAt: wall}, now); got != SyncNoop {
		t.Fatalf("got %v, want noop", got)
	}
}

func TestCheckNotFuture(t *testing.T) {
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	clk := clock.NewFake(now)

	tests := []struct {
		name     string
		incoming time.Time
		reject   bool
	}{
		{"same as now", now, false},
		{"past", now.Add(-24 * time.Hour), false},
		{"far past", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"zero time", time.Time{}, false},
		{"1 minute ahead", now.Add(time.Minute), false},
		{"4m59s ahead", now.Add(5*time.Minute - time.Second), false},
		{"exactly at the tolerance", now.Add(ClockSkewTolerance), false},
		{"1 ns past the tolerance is the same µs", now.Add(ClockSkewTolerance + time.Nanosecond), false},
		{"1 µs past the tolerance", now.Add(ClockSkewTolerance + time.Microsecond), true},
		{"5m01s ahead", now.Add(5*time.Minute + time.Second), true},
		{"a day ahead", now.Add(24 * time.Hour), true},
		{"year 9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"other zone, exactly at the tolerance", now.Add(ClockSkewTolerance).In(time.FixedZone("UTC+7", 7*3600)), false},
		{"other zone, 1 s past the tolerance", now.Add(ClockSkewTolerance + time.Second).In(time.FixedZone("UTC-5", -5*3600)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckNotFuture(tc.incoming, clk.Now())
			if !tc.reject {
				if err != nil {
					t.Fatalf("CheckNotFuture = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || !errors.Is(err, ErrValidation) {
				t.Fatalf("CheckNotFuture = %v (%T), want *ValidationError", err, err)
			}
			if len(ve.Issues) != 1 || ve.Issues[0] != (FieldIssue{Field: "updated_at", Issue: IssueTooFarInFuture}) {
				t.Fatalf("issues = %+v, want [{updated_at too_far_in_future}]", ve.Issues)
			}
		})
	}
}

func TestCheckNotFutureFollowsTheClock(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC))
	incoming := clk.Now().Add(10 * time.Minute)

	if err := CheckNotFuture(incoming, clk.Now()); err == nil {
		t.Fatal("10 minutes ahead must be rejected")
	}
	clk.Advance(5*time.Minute + time.Microsecond)
	if err := CheckNotFuture(incoming, clk.Now()); err != nil {
		t.Fatalf("after the clock caught up to within the tolerance: %v", err)
	}
	clk.Set(clk.Now().Add(-time.Hour)) // clock moves backwards
	if err := CheckNotFuture(incoming, clk.Now()); err == nil {
		t.Fatal("clock moved back, incoming is now far ahead, must be rejected")
	}
}

func TestCheckNotFutureSubMicrosecondNow(t *testing.T) {
	// now has nanoseconds (clock.Real): the boundary is still "exactly
	// tolerance ahead is fine", however many ns now carries.
	now := time.Date(2026, 9, 19, 10, 0, 0, 987_654_321, time.UTC)
	if err := CheckNotFuture(now.Add(ClockSkewTolerance), now); err != nil {
		t.Fatalf("exactly at tolerance with ns in now: %v", err)
	}
	if err := CheckNotFuture(now.Add(ClockSkewTolerance+time.Microsecond), now); err == nil {
		t.Fatal("1 µs past the tolerance must be rejected")
	}
}

func TestSyncActionErr(t *testing.T) {
	current := map[string]string{"id": "x"}

	stale := SyncStale.Err(current)
	var ce *ConflictError
	if !errors.As(stale, &ce) || !errors.Is(stale, ErrConflict) {
		t.Fatalf("SyncStale.Err = %v, want *ConflictError", stale)
	}
	if ce.Issue != IssueStale {
		t.Fatalf("stale issue = %q, want %q", ce.Issue, IssueStale)
	}
	if got, ok := ce.Current.(map[string]string); !ok || got["id"] != "x" {
		t.Fatalf("stale Current = %#v, want the server copy", ce.Current)
	}

	del := SyncDeleted.Err(current)
	if !errors.As(del, &ce) || ce.Issue != IssueDeleted {
		t.Fatalf("SyncDeleted.Err = %v, want conflict %q", del, IssueDeleted)
	}
	if ce.Current != nil {
		t.Fatalf("deleted conflict must not carry current, got %#v", ce.Current)
	}

	for _, a := range []SyncAction{SyncInsert, SyncUpdate, SyncNoop} {
		if err := a.Err(current); err != nil {
			t.Errorf("%v.Err = %v, want nil", a, err)
		}
	}
}

func TestSyncActionString(t *testing.T) {
	want := map[SyncAction]string{
		SyncInsert: "insert", SyncUpdate: "update", SyncNoop: "noop",
		SyncStale: "stale", SyncDeleted: "deleted", SyncAction(0): "SyncAction(0)",
		SyncAction(99): "SyncAction(99)",
	}
	for a, s := range want {
		if got := a.String(); got != s {
			t.Errorf("SyncAction(%d).String() = %q, want %q", int(a), got, s)
		}
	}
	// The zero value is not a valid decision.
	if SyncAction(0) == SyncInsert || SyncAction(0) == SyncNoop {
		t.Fatal("the zero SyncAction must not equal a real action")
	}
}
