package httpapi

import (
	"net/http"
	"slices"

	"workout-tracker-be/internal/domain"
)

// Route describes one route of the API: how it is matched and which
// middleware guard it. Routes() is the machine-readable form of the access
// matrix in docs/api/conventions.md, so role-matrix tests can iterate over it.
type Route struct {
	// Number is the endpoint number in docs/README.md (1-21); 0 for /healthz.
	Number int
	// Name is the operation, equal to its field name in Handlers.
	Name   string
	Method string
	// Path is the ServeMux path, with {wildcards}.
	Path string
	// Anonymous routes need no token (login, healthz). All others go through
	// the authenticator and RequireRoles(Roles...).
	Anonymous bool
	// Roles may call the route. Empty for anonymous routes.
	Roles []domain.Role
	// AuthRateLimited routes pass the generic per-IP rate limiter.
	AuthRateLimited bool
	// MaxBodyBytes is the request body limit.
	MaxBodyBytes int64
}

// Pattern is the ServeMux pattern, for example "PUT /v1/progress/{id}".
func (r Route) Pattern() string { return r.Method + " " + r.Path }

// Routes returns every route, the 21 endpoints in README order plus /healthz.
// The result is a copy.
func Routes() []Route {
	out := make([]Route, len(table))
	for i, rt := range table {
		out[i] = rt.Route
		out[i].Roles = slices.Clone(rt.Roles)
	}
	return out
}

// route is a Route plus the Handlers field that serves it.
type route struct {
	Route
	field func(*Handlers) *http.HandlerFunc
}

var (
	rolesUser  = []domain.Role{domain.RoleUser}
	rolesAdmin = []domain.Role{domain.RoleAdmin}
	rolesBoth  = []domain.Role{domain.RoleUser, domain.RoleAdmin}
)

// table is the single place that lists the routes. Access matrix (conventions,
// "Roles & access"): register, admin-revoke-login and admin-change-password
// are admin only; login and healthz are anonymous; logout is for both roles;
// everything else is user only.
var table = withDefaults([]route{
	{Route{Number: 1, Name: "Register", Method: http.MethodPost, Path: "/v1/auth/register", Roles: rolesAdmin, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.Register }},
	{Route{Number: 2, Name: "Login", Method: http.MethodPost, Path: "/v1/auth/login", Anonymous: true, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.Login }},
	{Route{Number: 3, Name: "SaveProgress", Method: http.MethodPut, Path: "/v1/progress/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.SaveProgress }},
	{Route{Number: 4, Name: "GetProgress", Method: http.MethodGet, Path: "/v1/progress/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.GetProgress }},
	{Route{Number: 5, Name: "ListProgress", Method: http.MethodGet, Path: "/v1/progress", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.ListProgress }},
	{Route{Number: 6, Name: "ListWorkoutPlans", Method: http.MethodGet, Path: "/v1/workout-plans", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.ListWorkoutPlans }},
	{Route{Number: 7, Name: "GetWorkoutPlan", Method: http.MethodGet, Path: "/v1/workout-plans/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.GetWorkoutPlan }},
	{Route{Number: 8, Name: "ListExercises", Method: http.MethodGet, Path: "/v1/exercises", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.ListExercises }},
	{Route{Number: 9, Name: "CreateExercise", Method: http.MethodPost, Path: "/v1/exercises", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.CreateExercise }},
	{Route{Number: 10, Name: "SaveWorkoutPlan", Method: http.MethodPut, Path: "/v1/workout-plans/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.SaveWorkoutPlan }},
	{Route{Number: 11, Name: "Logout", Method: http.MethodPost, Path: "/v1/auth/logout", Roles: rolesBoth, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.Logout }},
	{Route{Number: 12, Name: "RevokeTokens", Method: http.MethodPost, Path: "/v1/auth/revoke", Roles: rolesUser, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.RevokeTokens }},
	{Route{Number: 13, Name: "AdminRevokeLogin", Method: http.MethodPost, Path: "/v1/admin/users/{username}/revoke-login", Roles: rolesAdmin, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.AdminRevokeLogin }},
	{Route{Number: 14, Name: "AdminChangePassword", Method: http.MethodPut, Path: "/v1/admin/users/{username}/password", Roles: rolesAdmin, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.AdminChangePassword }},
	{Route{Number: 15, Name: "ListTokens", Method: http.MethodGet, Path: "/v1/auth/tokens", Roles: rolesUser, AuthRateLimited: true},
		func(h *Handlers) *http.HandlerFunc { return &h.ListTokens }},
	{Route{Number: 16, Name: "DeleteProgress", Method: http.MethodDelete, Path: "/v1/progress/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.DeleteProgress }},
	{Route{Number: 17, Name: "DeleteWorkoutPlan", Method: http.MethodDelete, Path: "/v1/workout-plans/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.DeleteWorkoutPlan }},
	{Route{Number: 18, Name: "UpdateExercise", Method: http.MethodPut, Path: "/v1/exercises/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.UpdateExercise }},
	{Route{Number: 19, Name: "DeleteExercise", Method: http.MethodDelete, Path: "/v1/exercises/{id}", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.DeleteExercise }},
	{Route{Number: 20, Name: "SetExerciseImage", Method: http.MethodPut, Path: "/v1/exercises/{id}/image", Roles: rolesUser, MaxBodyBytes: domain.MaxImageBytes},
		func(h *Handlers) *http.HandlerFunc { return &h.SetExerciseImage }},
	{Route{Number: 21, Name: "DeleteExerciseImage", Method: http.MethodDelete, Path: "/v1/exercises/{id}/image", Roles: rolesUser},
		func(h *Handlers) *http.HandlerFunc { return &h.DeleteExerciseImage }},
	{Route{Number: 0, Name: "Healthz", Method: http.MethodGet, Path: "/healthz", Anonymous: true},
		func(h *Handlers) *http.HandlerFunc { return &h.Healthz }},
})

// withDefaults sets the default body limit on routes that do not override it.
func withDefaults(rs []route) []route {
	for i := range rs {
		if rs[i].MaxBodyBytes == 0 {
			rs[i].MaxBodyBytes = domain.MaxBodyBytes
		}
	}
	return rs
}
