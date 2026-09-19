-- +goose Up
CREATE TABLE progress (
    id                uuid        NOT NULL,
    user_id           uuid        NOT NULL,
    -- Freestyle workout when NULL. No ON DELETE action: plans are only soft-deleted.
    workout_plan_id   uuid,
    name              text        NOT NULL,
    notes             text,
    started_at        timestamptz NOT NULL,
    ended_at          timestamptz NOT NULL,
    duration_seconds  integer     NOT NULL,
    -- API field updated_at, drives the conflict rule.
    client_updated_at timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- Sync cursor. Set by the application on every accepted write and delete.
    server_updated_at timestamptz NOT NULL,
    deleted_at        timestamptz,

    CONSTRAINT progress_pkey PRIMARY KEY (id),
    CONSTRAINT progress_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT progress_workout_plan_id_fkey FOREIGN KEY (workout_plan_id) REFERENCES workout_plans (id),
    CONSTRAINT progress_name_len_chk CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT progress_notes_len_chk CHECK (char_length(notes) <= 2000),
    CONSTRAINT progress_ended_at_chk CHECK (ended_at >= started_at),
    CONSTRAINT progress_duration_seconds_chk CHECK (duration_seconds BETWEEN 0 AND 86400)
);

-- Default listing order and sync feed.
CREATE INDEX progress_user_id_started_at_id_idx ON progress (user_id, started_at DESC, id);
CREATE INDEX progress_user_id_server_updated_at_id_idx ON progress (user_id, server_updated_at, id);
CREATE INDEX progress_workout_plan_id_idx ON progress (workout_plan_id);

CREATE TABLE progress_exercises (
    id          uuid    NOT NULL,
    progress_id uuid    NOT NULL,
    -- No ON DELETE action: a soft-deleted exercise is still a valid reference.
    exercise_id uuid    NOT NULL,
    position    integer NOT NULL,
    notes       text,

    CONSTRAINT progress_exercises_pkey PRIMARY KEY (id),
    CONSTRAINT progress_exercises_progress_id_fkey
        FOREIGN KEY (progress_id) REFERENCES progress (id) ON DELETE CASCADE,
    CONSTRAINT progress_exercises_exercise_id_fkey
        FOREIGN KEY (exercise_id) REFERENCES exercises (id),
    CONSTRAINT progress_exercises_progress_id_position_uniq UNIQUE (progress_id, position),
    CONSTRAINT progress_exercises_position_chk CHECK (position >= 0),
    CONSTRAINT progress_exercises_notes_len_chk CHECK (char_length(notes) <= 1000)
);

-- Future per-exercise history and stats.
CREATE INDEX progress_exercises_exercise_id_idx ON progress_exercises (exercise_id);

CREATE TABLE progress_sets (
    id                   uuid         NOT NULL,
    progress_exercise_id uuid         NOT NULL,
    position             integer      NOT NULL,
    type                 text         NOT NULL,
    reps                 integer,
    weight               numeric(7,2),
    duration_seconds     integer,
    distance_meters      numeric(9,2),
    rpe                  numeric(3,1),
    completed            boolean      NOT NULL,

    CONSTRAINT progress_sets_pkey PRIMARY KEY (id),
    CONSTRAINT progress_sets_progress_exercise_id_fkey
        FOREIGN KEY (progress_exercise_id) REFERENCES progress_exercises (id) ON DELETE CASCADE,
    CONSTRAINT progress_sets_progress_exercise_id_position_uniq UNIQUE (progress_exercise_id, position),
    CONSTRAINT progress_sets_position_chk CHECK (position >= 0),
    -- Values are domain.SetTypes.
    CONSTRAINT progress_sets_type_chk CHECK (type IN ('warmup', 'normal', 'drop', 'failure')),
    CONSTRAINT progress_sets_reps_chk CHECK (reps >= 0),
    CONSTRAINT progress_sets_weight_chk CHECK (weight >= 0),
    CONSTRAINT progress_sets_duration_seconds_chk CHECK (duration_seconds >= 0),
    CONSTRAINT progress_sets_distance_meters_chk CHECK (distance_meters >= 0),
    -- RPE is 1 to 10 in steps of 0.5.
    CONSTRAINT progress_sets_rpe_range_chk CHECK (rpe BETWEEN 1 AND 10),
    CONSTRAINT progress_sets_rpe_step_chk CHECK (mod(rpe, 0.5) = 0)
);

-- +goose Down
DROP TABLE progress_sets;
DROP TABLE progress_exercises;
DROP TABLE progress;
