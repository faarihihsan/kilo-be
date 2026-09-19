package handlers

import (
	"net/http"

	"workout-tracker-be/internal/service"
)

// Compile-time pin of the seam signatures fixed by T0.9. cmd/server wires
// these constructors and methods by name; a task that changes one of them
// breaks this file (and the build of cmd/server) instead of the merge.
var (
	_ func(*service.Auth, Deps) *Auth                     = NewAuth
	_ func(*service.Admin, Deps) *Admin                   = NewAdmin
	_ func(*service.Exercises, Deps) *Exercises           = NewExercises
	_ func(*service.ExerciseImages, Deps) *ExerciseImages = NewExerciseImages
	_ func(*service.Plans, Deps) *Plans                   = NewPlans
	_ func(*service.Progress, Deps) *Progress             = NewProgress

	// A method expression has the receiver as first parameter, so this also
	// checks the (w http.ResponseWriter, r *http.Request) shape.
	_ = []func(*Auth, http.ResponseWriter, *http.Request){
		(*Auth).Login, (*Auth).Logout, (*Auth).RevokeTokens, (*Auth).ListTokens,
	}
	_ = []func(*Admin, http.ResponseWriter, *http.Request){
		(*Admin).Register, (*Admin).RevokeLogin, (*Admin).ChangePassword,
	}
	_ = []func(*Exercises, http.ResponseWriter, *http.Request){
		(*Exercises).List, (*Exercises).Create, (*Exercises).Update, (*Exercises).Delete,
	}
	_ = []func(*ExerciseImages, http.ResponseWriter, *http.Request){
		(*ExerciseImages).Set, (*ExerciseImages).Delete,
	}
	_ = []func(*Plans, http.ResponseWriter, *http.Request){
		(*Plans).List, (*Plans).Get, (*Plans).Save, (*Plans).Delete,
	}
	_ = []func(*Progress, http.ResponseWriter, *http.Request){
		(*Progress).List, (*Progress).Get, (*Progress).Save, (*Progress).Delete,
	}
)
