// Package middleware holds the HTTP middleware of the API and the request
// context helpers they share. Every constructor returns
// func(http.Handler) http.Handler, so the router can chain them in the order
// of docs/implementation-plan.md section 4.
//
// The token authenticator (auth.go) and the per-IP rate limiter
// (ratelimit.go) are added by the auth task; they plug into the router
// through its RouterConfig slots.
package middleware
