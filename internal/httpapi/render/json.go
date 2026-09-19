package render

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"workout-tracker-be/internal/domain"
)

// ContentTypeJSON is the Content-Type of every JSON response.
const ContentTypeJSON = "application/json; charset=utf-8"

// WriteJSON writes v as the JSON response body with the given status. The
// body is encoded before anything is sent, so an encoding failure becomes a
// clean 500 instead of a half-written response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", slog.Any("error", err))
		status = http.StatusInternalServerError
		b, _ = json.Marshal(NewErrorBody(CodeInternal, MsgInternal))
	}
	h := w.Header()
	h.Set("Content-Type", ContentTypeJSON)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// NoContent answers 204 with no body.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// DecodeJSON reads the request body into dst (a pointer) as exactly one JSON
// value. It is strict:
//
//   - a missing or empty body is a 400;
//   - a body with a Content-Type other than application/json (charset, when
//     given, must be utf-8) is a 415;
//   - unknown fields, malformed JSON, values of the wrong JSON type and any
//     data after the first value are a 400;
//   - a body over the limit is a 413.
//
// The errors are domain errors (or *http.MaxBytesError), ready for WriteError.
// The router's BodyLimit middleware sets the real per-route limit; DecodeJSON
// also caps the read at domain.MaxBodyBytes so it is safe on its own. It is
// meant for JSON routes, not for the raw image upload.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return domain.NewBadRequest("The request body must not be empty.")
	}
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, domain.MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	// Anything but whitespace after the value is an error.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return err
		}
		return domain.NewBadRequest("The request body must contain a single JSON value.")
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &domain.UnsupportedMediaTypeError{Message: "Content-Type must be application/json."}
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return &domain.UnsupportedMediaTypeError{Message: "The charset must be utf-8."}
	}
	return nil
}

// decodeError turns an error of json.Decoder.Decode into what the client
// should see.
func decodeError(err error) error {
	var (
		maxBytes  *http.MaxBytesError
		invalidIn *json.InvalidUnmarshalError
	)
	switch {
	case errors.As(err, &maxBytes):
		return err // WriteError maps it to 413
	case errors.As(err, &invalidIn):
		return err // a bug in the caller (dst is not a pointer): a 500
	case errors.Is(err, io.EOF):
		return domain.NewBadRequest("The request body must not be empty.")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return domain.NewBadRequest("Malformed JSON: unexpected end of input.")
	}
	if br := jsonBadRequest(err); br != nil {
		return br
	}
	// What is left is a failed read of the body (client gone, connection reset).
	return domain.NewBadRequest("The request body could not be read.")
}
