package domain

import "slices"

// Enum values are fixed lists validated by the server (docs/api/enums.md);
// anything else is a 422 with issue invalid_value. Each enum has a string type,
// one constant per value, an ordered slice of all values (spec order) and an
// IsValid method. The exported slices are read-only: never modify them.
//
// The same lists back the CHECK constraints in the migrations, so a value is
// added here and in a new migration together.

// Role is a user's role. It is fixed in code, not user-facing data.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

var Roles = []Role{RoleUser, RoleAdmin}

func (r Role) IsValid() bool { return slices.Contains(Roles, r) }

// Category is the exercise category.
type Category string

const (
	CategoryStrength Category = "strength"
	CategoryCardio   Category = "cardio"
	CategoryMobility Category = "mobility"
)

var Categories = []Category{CategoryStrength, CategoryCardio, CategoryMobility}

func (c Category) IsValid() bool { return slices.Contains(Categories, c) }

// MuscleGroup is used by an exercise's primary_muscle_group and
// secondary_muscle_groups[].
type MuscleGroup string

const (
	MuscleGroupChest      MuscleGroup = "chest"
	MuscleGroupUpperBack  MuscleGroup = "upper_back"
	MuscleGroupLats       MuscleGroup = "lats"
	MuscleGroupLowerBack  MuscleGroup = "lower_back"
	MuscleGroupTraps      MuscleGroup = "traps"
	MuscleGroupShoulders  MuscleGroup = "shoulders"
	MuscleGroupBiceps     MuscleGroup = "biceps"
	MuscleGroupTriceps    MuscleGroup = "triceps"
	MuscleGroupForearms   MuscleGroup = "forearms"
	MuscleGroupAbs        MuscleGroup = "abs"
	MuscleGroupObliques   MuscleGroup = "obliques"
	MuscleGroupGlutes     MuscleGroup = "glutes"
	MuscleGroupQuads      MuscleGroup = "quads"
	MuscleGroupHamstrings MuscleGroup = "hamstrings"
	MuscleGroupCalves     MuscleGroup = "calves"
	MuscleGroupAdductors  MuscleGroup = "adductors"
	MuscleGroupAbductors  MuscleGroup = "abductors"
	MuscleGroupFullBody   MuscleGroup = "full_body"
)

var MuscleGroups = []MuscleGroup{
	MuscleGroupChest, MuscleGroupUpperBack, MuscleGroupLats, MuscleGroupLowerBack,
	MuscleGroupTraps, MuscleGroupShoulders, MuscleGroupBiceps, MuscleGroupTriceps,
	MuscleGroupForearms, MuscleGroupAbs, MuscleGroupObliques, MuscleGroupGlutes,
	MuscleGroupQuads, MuscleGroupHamstrings, MuscleGroupCalves, MuscleGroupAdductors,
	MuscleGroupAbductors, MuscleGroupFullBody,
}

func (m MuscleGroup) IsValid() bool { return slices.Contains(MuscleGroups, m) }

// Equipment is the exercise equipment. Its list is EquipmentList because
// "Equipments" is not a word.
type Equipment string

const (
	EquipmentBarbell        Equipment = "barbell"
	EquipmentDumbbell       Equipment = "dumbbell"
	EquipmentKettlebell     Equipment = "kettlebell"
	EquipmentMachine        Equipment = "machine"
	EquipmentCable          Equipment = "cable"
	EquipmentSmithMachine   Equipment = "smith_machine"
	EquipmentBodyweight     Equipment = "bodyweight"
	EquipmentResistanceBand Equipment = "resistance_band"
	EquipmentCardioMachine  Equipment = "cardio_machine"
	EquipmentOther          Equipment = "other"
)

var EquipmentList = []Equipment{
	EquipmentBarbell, EquipmentDumbbell, EquipmentKettlebell, EquipmentMachine,
	EquipmentCable, EquipmentSmithMachine, EquipmentBodyweight,
	EquipmentResistanceBand, EquipmentCardioMachine, EquipmentOther,
}

func (e Equipment) IsValid() bool { return slices.Contains(EquipmentList, e) }

// MeasurementType tells the app which set fields to show and log.
type MeasurementType string

const (
	// MeasurementTypeRepsWeight uses reps and weight (bench press).
	MeasurementTypeRepsWeight MeasurementType = "reps_weight"
	// MeasurementTypeReps uses reps only (pull-ups).
	MeasurementTypeReps MeasurementType = "reps"
	// MeasurementTypeDuration uses duration_seconds (plank).
	MeasurementTypeDuration MeasurementType = "duration"
	// MeasurementTypeDistanceDuration uses distance_meters and
	// duration_seconds (running).
	MeasurementTypeDistanceDuration MeasurementType = "distance_duration"
)

var MeasurementTypes = []MeasurementType{
	MeasurementTypeRepsWeight, MeasurementTypeReps,
	MeasurementTypeDuration, MeasurementTypeDistanceDuration,
}

func (m MeasurementType) IsValid() bool { return slices.Contains(MeasurementTypes, m) }

// SetType is the type of a progress set.
type SetType string

const (
	SetTypeWarmup  SetType = "warmup"
	SetTypeNormal  SetType = "normal"
	SetTypeDrop    SetType = "drop"
	SetTypeFailure SetType = "failure"
)

var SetTypes = []SetType{SetTypeWarmup, SetTypeNormal, SetTypeDrop, SetTypeFailure}

func (s SetType) IsValid() bool { return slices.Contains(SetTypes, s) }

// ImageExt is the file extension of a stored exercise image, derived from the
// sniffed content type (docs/api/endpoints/20-set-exercise-image.md).
type ImageExt string

const (
	ImageExtJPG  ImageExt = "jpg"
	ImageExtPNG  ImageExt = "png"
	ImageExtWebP ImageExt = "webp"
)

var ImageExts = []ImageExt{ImageExtJPG, ImageExtPNG, ImageExtWebP}

func (e ImageExt) IsValid() bool { return slices.Contains(ImageExts, e) }

// ContentType returns the media type served for the extension, or "" for an
// invalid extension.
func (e ImageExt) ContentType() string {
	switch e {
	case ImageExtJPG:
		return "image/jpeg"
	case ImageExtPNG:
		return "image/png"
	case ImageExtWebP:
		return "image/webp"
	}
	return ""
}
