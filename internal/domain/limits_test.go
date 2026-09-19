package domain

import (
	"math"
	"testing"
	"time"
)

// TestLimitsMatchSpecs pins every limit to the value written in the docs, so a
// change has to be deliberate and touch both places. Sources are in limits.go.
func TestLimitsMatchSpecs(t *testing.T) {
	ints := []struct {
		name      string
		got, want int64
	}{
		{"MaxBodyBytes", MaxBodyBytes, 1 << 20},
		{"MaxImageBytes", MaxImageBytes, 2 << 20},
		{"DefaultPageLimit", DefaultPageLimit, 50},
		{"MinPageLimit", MinPageLimit, 1},
		{"MaxPageLimit", MaxPageLimit, 200},
		{"UsernameMinLen", UsernameMinLen, 3},
		{"UsernameMaxLen", UsernameMaxLen, 30},
		{"PasswordMaxBytes", PasswordMaxBytes, 128},
		{"DeviceNameMaxLen", DeviceNameMaxLen, 100},
		{"TokenRandomBytes", TokenRandomBytes, 32},
		{"DefaultTokenTTLDays", DefaultTokenTTLDays, 365},
		{"ExerciseNameMinLen", ExerciseNameMinLen, 1},
		{"ExerciseNameMaxLen", ExerciseNameMaxLen, 100},
		{"ExerciseInstructionsMaxLen", ExerciseInstructionsMaxLen, 4000},
		{"MaxSecondaryMuscleGroups", MaxSecondaryMuscleGroups, 5},
		{"MaxImageDimension", MaxImageDimension, 2000},
		{"ImageHashLen", ImageHashLen, 16},
		{"PlanNameMinLen", PlanNameMinLen, 1},
		{"PlanNameMaxLen", PlanNameMaxLen, 100},
		{"PlanDescriptionMaxLen", PlanDescriptionMaxLen, 1000},
		{"PlanExerciseNotesMaxLen", PlanExerciseNotesMaxLen, 1000},
		{"MaxExercisesPerPlan", MaxExercisesPerPlan, 50},
		{"MaxActivePlansPerUser", MaxActivePlansPerUser, 100},
		{"PlanTargetSetsMin", PlanTargetSetsMin, 1},
		{"PlanTargetSetsMax", PlanTargetSetsMax, 20},
		{"PlanTargetRepsMin", PlanTargetRepsMin, 1},
		{"PlanRestSecondsMin", PlanRestSecondsMin, 0},
		{"PlanRestSecondsMax", PlanRestSecondsMax, 3600},
		{"ProgressNameMinLen", ProgressNameMinLen, 1},
		{"ProgressNameMaxLen", ProgressNameMaxLen, 100},
		{"ProgressNotesMaxLen", ProgressNotesMaxLen, 2000},
		{"ProgressExerciseNotesMaxLen", ProgressExerciseNotesMaxLen, 1000},
		{"MaxExercisesPerProgress", MaxExercisesPerProgress, 50},
		{"MaxSetsPerExercise", MaxSetsPerExercise, 100},
		{"ProgressDurationMinSeconds", ProgressDurationMinSeconds, 0},
		{"ProgressDurationMaxSeconds", ProgressDurationMaxSeconds, 86400},
		{"RPEMin", RPEMin, 1},
		{"RPEMax", RPEMax, 10},
		{"DecimalPlaces", DecimalPlaces, 2},
		{"MaxIntColumn", MaxIntColumn, math.MaxInt32},
	}
	for _, tc := range ints {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	if got := float64(RPEStep); got != 0.5 {
		t.Errorf("RPEStep = %v, want 0.5", got)
	}
	// numeric(7,2) and numeric(9,2): five and seven integer digits.
	if got := float64(MaxWeightKg); got != 99999.99 {
		t.Errorf("MaxWeightKg = %v, want 99999.99", got)
	}
	if got := float64(MaxDistanceMeters); got != 9999999.99 {
		t.Errorf("MaxDistanceMeters = %v, want 9999999.99", got)
	}

	durations := []struct {
		name      string
		got, want time.Duration
	}{
		{"ClockSkewTolerance", ClockSkewTolerance, 5 * time.Minute},
		{"DefaultTokenTTL", DefaultTokenTTL, 365 * 24 * time.Hour},
		{"TokenLastUsedInterval", TokenLastUsedInterval, time.Hour},
		{"TokenPurgeAfter", TokenPurgeAfter, 30 * 24 * time.Hour},
	}
	for _, tc := range durations {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	strs := []struct{ name, got, want string }{
		{"TokenPrefix", TokenPrefix, "wt_"},
		{"ExpandExercises", ExpandExercises, "exercises"},
	}
	for _, tc := range strs {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLimitsAreInternallyConsistent(t *testing.T) {
	if MinPageLimit > DefaultPageLimit || DefaultPageLimit > MaxPageLimit {
		t.Errorf("page limits not ordered: min %d, default %d, max %d", MinPageLimit, DefaultPageLimit, MaxPageLimit)
	}
	if MaxImageBytes < MaxBodyBytes {
		t.Errorf("image body limit %d is below the general limit %d", MaxImageBytes, MaxBodyBytes)
	}
	if PlanTargetSetsMin > PlanTargetSetsMax || PlanRestSecondsMin > PlanRestSecondsMax {
		t.Error("plan ranges are inverted")
	}
	// Session length caps must fit the int column.
	if ProgressDurationMaxSeconds > MaxIntColumn {
		t.Error("ProgressDurationMaxSeconds exceeds the integer column")
	}
}
