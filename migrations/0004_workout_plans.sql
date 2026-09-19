-- +goose Up
CREATE TABLE workout_plans (
    id                uuid        NOT NULL,
    user_id           uuid        NOT NULL,
    name              text        NOT NULL,
    description       text,
    -- API field updated_at, drives the conflict rule.
    client_updated_at timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- Sync cursor. Set by the application on every accepted write and delete.
    server_updated_at timestamptz NOT NULL,
    deleted_at        timestamptz,

    CONSTRAINT workout_plans_pkey PRIMARY KEY (id),
    CONSTRAINT workout_plans_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT workout_plans_name_len_chk CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT workout_plans_description_len_chk CHECK (char_length(description) <= 1000)
);

-- Sync feed.
CREATE INDEX workout_plans_user_id_server_updated_at_id_idx ON workout_plans (user_id, server_updated_at, id);
-- Default listing order.
CREATE INDEX workout_plans_user_id_lower_name_id_idx ON workout_plans (user_id, lower(name), id);

CREATE TABLE workout_plan_exercises (
    id                      uuid         NOT NULL,
    workout_plan_id         uuid         NOT NULL,
    -- No ON DELETE action: exercises are only ever soft-deleted, and a
    -- soft-deleted exercise is still a valid reference.
    exercise_id             uuid         NOT NULL,
    position                integer      NOT NULL,
    target_sets             integer      NOT NULL,
    target_reps             integer,
    target_reps_max         integer,
    target_weight           numeric(7,2),
    target_duration_seconds integer,
    target_distance_meters  numeric(9,2),
    rest_seconds            integer,
    notes                   text,

    CONSTRAINT workout_plan_exercises_pkey PRIMARY KEY (id),
    CONSTRAINT workout_plan_exercises_workout_plan_id_fkey
        FOREIGN KEY (workout_plan_id) REFERENCES workout_plans (id) ON DELETE CASCADE,
    CONSTRAINT workout_plan_exercises_exercise_id_fkey
        FOREIGN KEY (exercise_id) REFERENCES exercises (id),
    CONSTRAINT workout_plan_exercises_workout_plan_id_position_uniq UNIQUE (workout_plan_id, position),

    CONSTRAINT workout_plan_exercises_position_chk CHECK (position >= 0),
    CONSTRAINT workout_plan_exercises_target_sets_chk CHECK (target_sets BETWEEN 1 AND 20),
    CONSTRAINT workout_plan_exercises_target_reps_chk CHECK (target_reps >= 1),
    -- target_reps_max needs target_reps and must not be below it.
    CONSTRAINT workout_plan_exercises_target_reps_max_requires_reps_chk
        CHECK (target_reps_max IS NULL OR target_reps IS NOT NULL),
    CONSTRAINT workout_plan_exercises_target_reps_max_ge_reps_chk
        CHECK (target_reps_max >= target_reps),
    CONSTRAINT workout_plan_exercises_target_weight_chk CHECK (target_weight >= 0),
    CONSTRAINT workout_plan_exercises_target_duration_seconds_chk CHECK (target_duration_seconds >= 0),
    CONSTRAINT workout_plan_exercises_target_distance_meters_chk CHECK (target_distance_meters >= 0),
    CONSTRAINT workout_plan_exercises_rest_seconds_chk CHECK (rest_seconds BETWEEN 0 AND 3600),
    CONSTRAINT workout_plan_exercises_notes_len_chk CHECK (char_length(notes) <= 1000)
);

CREATE INDEX workout_plan_exercises_exercise_id_idx ON workout_plan_exercises (exercise_id);

-- +goose Down
DROP TABLE workout_plan_exercises;
DROP TABLE workout_plans;
