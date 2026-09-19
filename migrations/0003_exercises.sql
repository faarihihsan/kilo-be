-- +goose Up
CREATE TABLE exercises (
    id                      uuid        NOT NULL,
    name                    text        NOT NULL,
    category                text        NOT NULL,
    primary_muscle_group    text        NOT NULL,
    secondary_muscle_groups text[]      NOT NULL DEFAULT '{}',
    equipment               text        NOT NULL,
    measurement_type        text        NOT NULL,
    instructions            text,
    image_hash              text,
    image_ext               text,
    image_size_bytes        integer,
    created_by              uuid        NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz,

    CONSTRAINT exercises_pkey PRIMARY KEY (id),
    CONSTRAINT exercises_created_by_fkey FOREIGN KEY (created_by) REFERENCES users (id),

    CONSTRAINT exercises_name_len_chk CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT exercises_instructions_len_chk CHECK (char_length(instructions) <= 4000),

    -- Enum lists below are domain.Categories, MuscleGroups, EquipmentList,
    -- MeasurementTypes and ImageExts. schema_test.go fails when they drift.
    CONSTRAINT exercises_category_chk CHECK (category IN ('strength', 'cardio', 'mobility')),
    CONSTRAINT exercises_primary_muscle_group_chk CHECK (primary_muscle_group IN (
        'chest', 'upper_back', 'lats', 'lower_back', 'traps', 'shoulders',
        'biceps', 'triceps', 'forearms', 'abs', 'obliques', 'glutes', 'quads',
        'hamstrings', 'calves', 'adductors', 'abductors', 'full_body'
    )),
    -- Each element must be a muscle group. At most 5, no duplicates and not
    -- the primary are checked in the application (data-model.md).
    CONSTRAINT exercises_secondary_muscle_groups_chk CHECK (secondary_muscle_groups <@ ARRAY[
        'chest', 'upper_back', 'lats', 'lower_back', 'traps', 'shoulders',
        'biceps', 'triceps', 'forearms', 'abs', 'obliques', 'glutes', 'quads',
        'hamstrings', 'calves', 'adductors', 'abductors', 'full_body'
    ]::text[]),
    CONSTRAINT exercises_equipment_chk CHECK (equipment IN (
        'barbell', 'dumbbell', 'kettlebell', 'machine', 'cable', 'smith_machine',
        'bodyweight', 'resistance_band', 'cardio_machine', 'other'
    )),
    CONSTRAINT exercises_measurement_type_chk CHECK (measurement_type IN (
        'reps_weight', 'reps', 'duration', 'distance_duration'
    )),

    -- The three image columns are all NULL (no image) or all set.
    CONSTRAINT exercises_image_all_or_none_chk CHECK (num_nonnulls(image_hash, image_ext, image_size_bytes) IN (0, 3)),
    CONSTRAINT exercises_image_hash_format_chk CHECK (image_hash ~ '^[0-9a-f]{16}$'),
    CONSTRAINT exercises_image_ext_chk CHECK (image_ext IN ('jpg', 'png', 'webp'))
);

-- A name is unique among live exercises only, so it can be reused after a
-- soft delete. Violations surface as the constraint name exercises_name_lower_uniq.
CREATE UNIQUE INDEX exercises_name_lower_uniq ON exercises (lower(name)) WHERE deleted_at IS NULL;

-- Sync feed (updated_since) order.
CREATE INDEX exercises_updated_at_id_idx ON exercises (updated_at, id);

-- Filter by primary muscle group.
CREATE INDEX exercises_primary_muscle_group_idx ON exercises (primary_muscle_group);

-- Name search (q). Queries must compare lower(name), for example
-- lower(name) LIKE '%' || lower($1) || '%', or the index is not used.
CREATE INDEX exercises_name_lower_trgm_idx ON exercises USING gin (lower(name) gin_trgm_ops);

-- +goose Down
DROP TABLE exercises;
