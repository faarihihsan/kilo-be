package testutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
)

// Raw-SQL seed helpers. They insert straight into the tables so store,
// service and handler tests do not depend on each other's unfinished code.
//
// Each takes a store.Querier, so it works on a *store.DB from NewDB or inside a
// transaction, calls t.Helper, fails the test on any error and returns what
// tests need. Defaults are valid and unique, so calling a helper twice never
// collides; functional options override them.
//
// Every seed writes what the application would: UUID v7 ids, times in UTC at
// microsecond precision.

// Ptr returns a pointer to v, for optional fields such as SetSpec.Reps.
func Ptr[T any](v T) *T { return &v }

// now is the seed default for timestamps: UTC, microsecond precision (what
// PostgreSQL stores), so a value read back compares equal.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func newID(t testing.TB) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("testutil: new uuid: %v", err)
	}
	return id
}

func mustExec(t testing.TB, q store.Querier, sql string, args ...any) {
	t.Helper()
	if _, err := q.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("testutil: seed failed: %v\nSQL: %s", err, strings.Join(strings.Fields(sql), " "))
	}
}

// --- users ------------------------------------------------------------------

// DummyPasswordHash is a well-formed argon2id string that no password
// verifies against (its hash bytes are all zero). Users from SeedUser get it, so
// they cannot log in. A test that needs a working login creates the user with
// the real hasher and passes WithPasswordHash.
var DummyPasswordHash = "$argon2id$v=19$m=65536,t=2,p=1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)

type userSpec struct {
	username     string
	passwordHash string
}

// UserOption customises SeedUser.
type UserOption func(*userSpec)

// WithUsername sets the username. It must satisfy domain.UsernamePattern.
func WithUsername(username string) UserOption {
	return func(s *userSpec) { s.username = username }
}

// WithPasswordHash sets the stored password hash (default DummyPasswordHash).
func WithPasswordHash(hash string) UserOption {
	return func(s *userSpec) { s.passwordHash = hash }
}

// SeedUser inserts a user with the role and returns its id and its random
// username (user_ plus 12 hex chars, valid for the username pattern).
func SeedUser(t testing.TB, db store.Querier, role domain.Role, opts ...UserOption) (id uuid.UUID, username string) {
	t.Helper()
	spec := userSpec{username: "user_" + randHex(6), passwordHash: DummyPasswordHash}
	for _, o := range opts {
		o(&spec)
	}
	id = newID(t)
	mustExec(t, db,
		`INSERT INTO users (id, username, password_hash, role) VALUES ($1, $2, $3, $4)`,
		id, spec.username, spec.passwordHash, string(role))
	return id, spec.username
}

// --- auth tokens ------------------------------------------------------------

// HashToken returns the value stored in auth_tokens.token_hash: the SHA-256 of
// the raw token string exactly as the client sends it after "Bearer ", prefix
// included.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

type tokenSpec struct {
	deviceName *string
	createdAt  *time.Time
	expiresAt  time.Time
	lastUsedAt *time.Time
	revokedAt  *time.Time
}

// TokenOption customises SeedToken.
type TokenOption func(*tokenSpec)

// WithTokenDevice sets device_name (default NULL).
func WithTokenDevice(name string) TokenOption {
	return func(s *tokenSpec) { s.deviceName = &name }
}

// WithTokenCreatedAt sets created_at (default: the database's now()).
func WithTokenCreatedAt(at time.Time) TokenOption {
	return func(s *tokenSpec) { s.createdAt = &at }
}

// WithTokenExpiresAt sets expires_at (default: now + domain.DefaultTokenTTL).
func WithTokenExpiresAt(at time.Time) TokenOption {
	return func(s *tokenSpec) { s.expiresAt = at }
}

// WithTokenExpired makes the token expired an hour ago.
func WithTokenExpired() TokenOption {
	return WithTokenExpiresAt(now().Add(-time.Hour))
}

// WithTokenRevoked sets revoked_at to at (default NULL, an active token).
func WithTokenRevoked(at time.Time) TokenOption {
	return func(s *tokenSpec) { s.revokedAt = &at }
}

// WithTokenLastUsedAt sets last_used_at (default NULL).
func WithTokenLastUsedAt(at time.Time) TokenOption {
	return func(s *tokenSpec) { s.lastUsedAt = &at }
}

// SeedToken inserts an auth token for userID and returns the raw token, which a
// test sends as "Authorization: Bearer <raw>", and the token row id. The raw
// token has the real shape: domain.TokenPrefix plus base64url of 32 random
// bytes. Only its SHA-256 is stored (see HashToken).
func SeedToken(t testing.TB, db store.Querier, userID uuid.UUID, opts ...TokenOption) (rawToken string, tokenID uuid.UUID) {
	t.Helper()
	spec := tokenSpec{expiresAt: now().Add(domain.DefaultTokenTTL)}
	for _, o := range opts {
		o(&spec)
	}

	secret := make([]byte, domain.TokenRandomBytes)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("testutil: random token: %v", err)
	}
	rawToken = domain.TokenPrefix + base64.RawURLEncoding.EncodeToString(secret)

	tokenID = newID(t)
	mustExec(t, db, `
		INSERT INTO auth_tokens (id, user_id, token_hash, device_name, created_at, expires_at, last_used_at, revoked_at)
		VALUES ($1, $2, $3, $4, COALESCE($5::timestamptz, now()), $6, $7, $8)`,
		tokenID, userID, HashToken(rawToken), spec.deviceName, spec.createdAt, spec.expiresAt, spec.lastUsedAt, spec.revokedAt)
	return rawToken, tokenID
}

// --- exercises --------------------------------------------------------------

type exerciseSpec struct {
	id           uuid.UUID
	name         string
	category     domain.Category
	primary      domain.MuscleGroup
	secondary    []string
	equipment    domain.Equipment
	measurement  domain.MeasurementType
	instructions *string
	imageHash    *string
	imageExt     *string
	imageSize    *int
	createdAt    *time.Time
	updatedAt    *time.Time
	deletedAt    *time.Time
}

// ExerciseOption customises SeedExercise.
type ExerciseOption func(*exerciseSpec)

// WithExerciseID sets the id (default: a new UUID v7).
func WithExerciseID(id uuid.UUID) ExerciseOption {
	return func(s *exerciseSpec) { s.id = id }
}

// WithExerciseName sets the name (default: "Exercise " plus random hex).
func WithExerciseName(name string) ExerciseOption {
	return func(s *exerciseSpec) { s.name = name }
}

// WithExerciseCategory sets the category (default strength).
func WithExerciseCategory(c domain.Category) ExerciseOption {
	return func(s *exerciseSpec) { s.category = c }
}

// WithPrimaryMuscleGroup sets primary_muscle_group (default chest).
func WithPrimaryMuscleGroup(m domain.MuscleGroup) ExerciseOption {
	return func(s *exerciseSpec) { s.primary = m }
}

// WithSecondaryMuscleGroups sets secondary_muscle_groups (default none).
func WithSecondaryMuscleGroups(groups ...domain.MuscleGroup) ExerciseOption {
	return func(s *exerciseSpec) {
		s.secondary = make([]string, len(groups))
		for i, g := range groups {
			s.secondary[i] = string(g)
		}
	}
}

// WithEquipment sets the equipment (default barbell).
func WithEquipment(e domain.Equipment) ExerciseOption {
	return func(s *exerciseSpec) { s.equipment = e }
}

// WithMeasurementType sets the measurement type (default reps_weight).
func WithMeasurementType(m domain.MeasurementType) ExerciseOption {
	return func(s *exerciseSpec) { s.measurement = m }
}

// WithInstructions sets the instructions (default NULL).
func WithInstructions(text string) ExerciseOption {
	return func(s *exerciseSpec) { s.instructions = &text }
}

// WithExerciseImage sets the three image columns together (default: no image).
// hash must be 16 lowercase hex characters.
func WithExerciseImage(hash string, ext domain.ImageExt, sizeBytes int) ExerciseOption {
	return func(s *exerciseSpec) {
		e := string(ext)
		s.imageHash, s.imageExt, s.imageSize = &hash, &e, &sizeBytes
	}
}

// WithExerciseCreatedAt sets created_at (default: the database's now()).
func WithExerciseCreatedAt(at time.Time) ExerciseOption {
	return func(s *exerciseSpec) { s.createdAt = &at }
}

// WithExerciseUpdatedAt sets updated_at, the sync cursor (default: now()).
func WithExerciseUpdatedAt(at time.Time) ExerciseOption {
	return func(s *exerciseSpec) { s.updatedAt = &at }
}

// WithExerciseDeletedAt soft-deletes the exercise at the given time.
func WithExerciseDeletedAt(at time.Time) ExerciseOption {
	return func(s *exerciseSpec) { s.deletedAt = &at }
}

// SeedExercise inserts an exercise created by createdBy and returns its id.
func SeedExercise(t testing.TB, db store.Querier, createdBy uuid.UUID, opts ...ExerciseOption) uuid.UUID {
	t.Helper()
	spec := exerciseSpec{
		name:        "Exercise " + randHex(6),
		category:    domain.CategoryStrength,
		primary:     domain.MuscleGroupChest,
		secondary:   []string{}, // never nil: the column is NOT NULL
		equipment:   domain.EquipmentBarbell,
		measurement: domain.MeasurementTypeRepsWeight,
	}
	for _, o := range opts {
		o(&spec)
	}
	if spec.id == uuid.Nil {
		spec.id = newID(t)
	}
	mustExec(t, db, `
		INSERT INTO exercises (id, name, category, primary_muscle_group, secondary_muscle_groups,
			equipment, measurement_type, instructions, image_hash, image_ext, image_size_bytes,
			created_by, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			COALESCE($13::timestamptz, now()), COALESCE($14::timestamptz, now()), $15)`,
		spec.id, spec.name, string(spec.category), string(spec.primary), spec.secondary,
		string(spec.equipment), string(spec.measurement), spec.instructions,
		spec.imageHash, spec.imageExt, spec.imageSize,
		createdBy, spec.createdAt, spec.updatedAt, spec.deletedAt)
	return spec.id
}

// --- workout plans ----------------------------------------------------------

type planExerciseSpec struct {
	exerciseID     uuid.UUID
	position       *int
	targetSets     int
	targetReps     *int
	targetRepsMax  *int
	targetWeight   *float64
	targetDuration *int
	targetDistance *float64
	restSeconds    *int
	notes          *string
}

// PlanExerciseOption customises one exercise row of a seeded plan.
type PlanExerciseOption func(*planExerciseSpec)

// WithPlanExercisePosition sets the position (default: the index in the plan).
func WithPlanExercisePosition(p int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.position = &p }
}

// WithTargetSets sets target_sets (default 3).
func WithTargetSets(n int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetSets = n }
}

// WithTargetReps sets target_reps (default 10). Use WithoutTargetReps for NULL.
func WithTargetReps(n int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetReps = &n }
}

// WithoutTargetReps sets target_reps to NULL.
func WithoutTargetReps() PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetReps = nil }
}

// WithTargetRepsMax sets target_reps_max (default NULL); needs a target_reps.
func WithTargetRepsMax(n int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetRepsMax = &n }
}

// WithTargetWeight sets target_weight in kg (default NULL).
func WithTargetWeight(kg float64) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetWeight = &kg }
}

// WithTargetDuration sets target_duration_seconds (default NULL).
func WithTargetDuration(seconds int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetDuration = &seconds }
}

// WithTargetDistance sets target_distance_meters (default NULL).
func WithTargetDistance(meters float64) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.targetDistance = &meters }
}

// WithRestSeconds sets rest_seconds (default NULL).
func WithRestSeconds(seconds int) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.restSeconds = &seconds }
}

// WithPlanExerciseNotes sets the row's notes (default NULL).
func WithPlanExerciseNotes(notes string) PlanExerciseOption {
	return func(s *planExerciseSpec) { s.notes = &notes }
}

type planSpec struct {
	id              uuid.UUID
	name            string
	description     *string
	clientUpdatedAt time.Time
	serverUpdatedAt time.Time
	deletedAt       *time.Time
	exercises       []planExerciseSpec
}

// PlanOption customises SeedPlan.
type PlanOption func(*planSpec)

// WithPlanID sets the id (default: a new UUID v7).
func WithPlanID(id uuid.UUID) PlanOption {
	return func(s *planSpec) { s.id = id }
}

// WithPlanName sets the name (default: "Plan " plus random hex).
func WithPlanName(name string) PlanOption {
	return func(s *planSpec) { s.name = name }
}

// WithPlanDescription sets the description (default NULL).
func WithPlanDescription(text string) PlanOption {
	return func(s *planSpec) { s.description = &text }
}

// WithPlanClientUpdatedAt sets client_updated_at, the API updated_at that
// drives the conflict rule (default: now).
func WithPlanClientUpdatedAt(at time.Time) PlanOption {
	return func(s *planSpec) { s.clientUpdatedAt = at }
}

// WithPlanServerUpdatedAt sets server_updated_at, the sync cursor (default: now).
func WithPlanServerUpdatedAt(at time.Time) PlanOption {
	return func(s *planSpec) { s.serverUpdatedAt = at }
}

// WithPlanDeletedAt soft-deletes the plan at the given time.
func WithPlanDeletedAt(at time.Time) PlanOption {
	return func(s *planSpec) { s.deletedAt = &at }
}

// WithPlanExercise appends an exercise row (position = its index unless set).
// The exercise must already exist, see SeedExercise. Defaults: 3 sets of 10
// reps, everything else NULL.
func WithPlanExercise(exerciseID uuid.UUID, opts ...PlanExerciseOption) PlanOption {
	return func(s *planSpec) {
		ex := planExerciseSpec{exerciseID: exerciseID, targetSets: 3, targetReps: Ptr(10)}
		for _, o := range opts {
			o(&ex)
		}
		s.exercises = append(s.exercises, ex)
	}
}

// SeedPlan inserts a workout plan owned by userID, with the exercise rows given
// by WithPlanExercise, and returns its id.
func SeedPlan(t testing.TB, db store.Querier, userID uuid.UUID, opts ...PlanOption) uuid.UUID {
	t.Helper()
	at := now()
	spec := planSpec{name: "Plan " + randHex(6), clientUpdatedAt: at, serverUpdatedAt: at}
	for _, o := range opts {
		o(&spec)
	}
	if spec.id == uuid.Nil {
		spec.id = newID(t)
	}
	mustExec(t, db, `
		INSERT INTO workout_plans (id, user_id, name, description, client_updated_at, server_updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		spec.id, userID, spec.name, spec.description, spec.clientUpdatedAt, spec.serverUpdatedAt, spec.deletedAt)

	for i, ex := range spec.exercises {
		pos := i
		if ex.position != nil {
			pos = *ex.position
		}
		mustExec(t, db, `
			INSERT INTO workout_plan_exercises (id, workout_plan_id, exercise_id, position, target_sets,
				target_reps, target_reps_max, target_weight, target_duration_seconds,
				target_distance_meters, rest_seconds, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			newID(t), spec.id, ex.exerciseID, pos, ex.targetSets,
			ex.targetReps, ex.targetRepsMax, ex.targetWeight, ex.targetDuration,
			ex.targetDistance, ex.restSeconds, ex.notes)
	}
	return spec.id
}

// --- progress ---------------------------------------------------------------

// SetSpec describes one set of a seeded progress exercise. Nil pointers are NULL.
type SetSpec struct {
	Type            domain.SetType // default normal
	Reps            *int
	Weight          *float64
	DurationSeconds *int
	DistanceMeters  *float64
	RPE             *float64
	NotCompleted    bool // sets are completed unless this is true
}

type progressExerciseSpec struct {
	exerciseID uuid.UUID
	position   *int
	notes      *string
	sets       []SetSpec
}

// ProgressExerciseOption customises one exercise of a seeded progress session.
type ProgressExerciseOption func(*progressExerciseSpec)

// WithProgressExercisePosition sets the position (default: the index).
func WithProgressExercisePosition(p int) ProgressExerciseOption {
	return func(s *progressExerciseSpec) { s.position = &p }
}

// WithProgressExerciseNotes sets the exercise notes (default NULL).
func WithProgressExerciseNotes(notes string) ProgressExerciseOption {
	return func(s *progressExerciseSpec) { s.notes = &notes }
}

// WithSets appends n normal completed sets of 10 reps at 50 kg.
func WithSets(n int) ProgressExerciseOption {
	return func(s *progressExerciseSpec) {
		for range n {
			s.sets = append(s.sets, SetSpec{Type: domain.SetTypeNormal, Reps: Ptr(10), Weight: Ptr(50.0)})
		}
	}
}

// WithSet appends one set (position = its index).
func WithSet(set SetSpec) ProgressExerciseOption {
	return func(s *progressExerciseSpec) { s.sets = append(s.sets, set) }
}

type progressSpec struct {
	id              uuid.UUID
	name            string
	notes           *string
	planID          *uuid.UUID
	startedAt       time.Time
	durationSeconds int
	endedAt         *time.Time
	clientUpdatedAt time.Time
	serverUpdatedAt time.Time
	deletedAt       *time.Time
	exercises       []progressExerciseSpec
}

// ProgressOption customises SeedProgress.
type ProgressOption func(*progressSpec)

// WithProgressID sets the id (default: a new UUID v7).
func WithProgressID(id uuid.UUID) ProgressOption {
	return func(s *progressSpec) { s.id = id }
}

// WithProgressName sets the name (default: "Workout " plus random hex).
func WithProgressName(name string) ProgressOption {
	return func(s *progressSpec) { s.name = name }
}

// WithProgressNotes sets the notes (default NULL).
func WithProgressNotes(text string) ProgressOption {
	return func(s *progressSpec) { s.notes = &text }
}

// WithProgressPlan links the session to a plan (default NULL, freestyle).
func WithProgressPlan(planID uuid.UUID) ProgressOption {
	return func(s *progressSpec) { s.planID = &planID }
}

// WithProgressStartedAt sets started_at (default: an hour ago). ended_at
// follows as started_at plus the duration unless WithProgressEndedAt is used.
func WithProgressStartedAt(at time.Time) ProgressOption {
	return func(s *progressSpec) { s.startedAt = at }
}

// WithProgressEndedAt sets ended_at explicitly.
func WithProgressEndedAt(at time.Time) ProgressOption {
	return func(s *progressSpec) { s.endedAt = &at }
}

// WithProgressDuration sets duration_seconds (default 3600).
func WithProgressDuration(seconds int) ProgressOption {
	return func(s *progressSpec) { s.durationSeconds = seconds }
}

// WithProgressClientUpdatedAt sets client_updated_at (default: now).
func WithProgressClientUpdatedAt(at time.Time) ProgressOption {
	return func(s *progressSpec) { s.clientUpdatedAt = at }
}

// WithProgressServerUpdatedAt sets server_updated_at, the sync cursor (default: now).
func WithProgressServerUpdatedAt(at time.Time) ProgressOption {
	return func(s *progressSpec) { s.serverUpdatedAt = at }
}

// WithProgressDeletedAt soft-deletes the session at the given time.
func WithProgressDeletedAt(at time.Time) ProgressOption {
	return func(s *progressSpec) { s.deletedAt = &at }
}

// WithProgressExercise appends an exercise (position = its index unless set)
// with the sets given by WithSets and WithSet. The exercise must already exist.
func WithProgressExercise(exerciseID uuid.UUID, opts ...ProgressExerciseOption) ProgressOption {
	return func(s *progressSpec) {
		ex := progressExerciseSpec{exerciseID: exerciseID}
		for _, o := range opts {
			o(&ex)
		}
		s.exercises = append(s.exercises, ex)
	}
}

// SeedProgress inserts a completed workout session of userID with its
// exercises and sets, and returns its id.
func SeedProgress(t testing.TB, db store.Querier, userID uuid.UUID, opts ...ProgressOption) uuid.UUID {
	t.Helper()
	at := now()
	spec := progressSpec{
		name:            "Workout " + randHex(6),
		startedAt:       at.Add(-time.Hour),
		durationSeconds: 3600,
		clientUpdatedAt: at,
		serverUpdatedAt: at,
	}
	for _, o := range opts {
		o(&spec)
	}
	if spec.id == uuid.Nil {
		spec.id = newID(t)
	}
	endedAt := spec.startedAt.Add(time.Duration(spec.durationSeconds) * time.Second)
	if spec.endedAt != nil {
		endedAt = *spec.endedAt
	}
	mustExec(t, db, `
		INSERT INTO progress (id, user_id, workout_plan_id, name, notes, started_at, ended_at,
			duration_seconds, client_updated_at, server_updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		spec.id, userID, spec.planID, spec.name, spec.notes, spec.startedAt, endedAt,
		spec.durationSeconds, spec.clientUpdatedAt, spec.serverUpdatedAt, spec.deletedAt)

	for i, ex := range spec.exercises {
		pos := i
		if ex.position != nil {
			pos = *ex.position
		}
		peID := newID(t)
		mustExec(t, db, `
			INSERT INTO progress_exercises (id, progress_id, exercise_id, position, notes)
			VALUES ($1, $2, $3, $4, $5)`,
			peID, spec.id, ex.exerciseID, pos, ex.notes)

		for j, set := range ex.sets {
			typ := set.Type
			if typ == "" {
				typ = domain.SetTypeNormal
			}
			mustExec(t, db, `
				INSERT INTO progress_sets (id, progress_exercise_id, position, type, reps, weight,
					duration_seconds, distance_meters, rpe, completed)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				newID(t), peID, j, string(typ), set.Reps, set.Weight,
				set.DurationSeconds, set.DistanceMeters, set.RPE, !set.NotCompleted)
		}
	}
	return spec.id
}

// --- truncate ---------------------------------------------------------------

// Truncate empties the named tables (and, through foreign keys, every table
// that references them: truncating users empties all data). With no names it
// empties every application table and keeps goose's bookkeeping table.
func Truncate(t testing.TB, db store.Querier, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		rows, err := db.Query(t.Context(), `
			SELECT tablename FROM pg_tables
			WHERE schemaname = current_schema() AND tablename <> 'goose_db_version'`)
		if err != nil {
			t.Fatalf("testutil: list tables: %v", err)
		}
		tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("testutil: list tables: %v", err)
		}
		if len(tables) == 0 {
			return
		}
	}
	idents := make([]string, len(tables))
	for i, name := range tables {
		idents[i] = quoteIdent(name)
	}
	mustExec(t, db, "TRUNCATE TABLE "+strings.Join(idents, ", ")+" RESTART IDENTITY CASCADE")
}
