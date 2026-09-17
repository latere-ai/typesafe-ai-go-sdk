// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// maxErrorBody caps how much of a failure response is read and kept. An error
// body is unstructured text in the worst case, and a gateway between the
// caller and the API can answer with a full HTML page.
const maxErrorBody = 64 << 10

// statusOverloaded is the status the API uses for a temporary overload. The
// standard library has no constant for it.
const statusOverloaded = 529

// APIError is a response the API answered with a status outside 2xx.
//
// Every field is filled on a best effort basis: Type and Message come from the
// response body, whose shape varies with which layer rejected the request, and
// Body keeps the bytes as they arrived so a caller can read whatever this type
// did not model.
type APIError struct {
	// StatusCode is the HTTP status of the response.
	StatusCode int
	// Type is the server's machine-readable category for the failure, taken
	// from the body's error_type field. It is empty when the body carries no
	// such field. The set of values is not documented, so it is carried as an
	// opaque string rather than as an enumeration.
	Type string
	// Message is the human-readable description of the failure. For a
	// validation failure that names several fields, it holds one entry per
	// field, joined with "; ".
	Message string
	// RequestID is the value of the x-typesafe-request-id response header,
	// which identifies the request in the server's own records.
	RequestID string
	// Body is the response body as it arrived, truncated to 64 KiB.
	Body []byte
}

// Error renders the status, the error type, the message and the request id.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("typesafe: http %d", e.StatusCode)
	if e.Type != "" {
		msg += " " + e.Type
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.RequestID != "" {
		msg += " (request id " + e.RequestID + ")"
	}
	return msg
}

// Temporary reports whether the same request may succeed if it is sent again.
// It is true for a request timeout, a rate limit and every server-side
// failure, which is the set the client retries on its own.
func (e *APIError) Temporary() bool {
	return e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode >= http.StatusInternalServerError
}

// newAPIError builds an APIError from one failure response.
func newAPIError(status int, requestID string, body []byte) *APIError {
	errType, message := parseErrorBody(body)
	if message == "" {
		message = statusText(status)
	}
	return &APIError{
		StatusCode: status,
		Type:       errType,
		Message:    message,
		RequestID:  requestID,
		Body:       body,
	}
}

// statusText names a status for an error whose body said nothing usable.
func statusText(status int) string {
	if s := http.StatusText(status); s != "" {
		return s
	}
	if status == statusOverloaded {
		return "Overloaded"
	}
	return "unexpected status"
}

// parseErrorBody reads the error type and the message out of a failure body.
//
// Three shapes reach a caller. The API's own envelope carries an object under
// detail, with error_type and message. The serving framework answers a routing
// failure with a plain string under detail, and a body validation failure with
// an array of entries under it. A body that is not JSON at all, such as a
// gateway's HTML page, is carried through as text.
func parseErrorBody(body []byte) (errType, message string) {
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", plainText(body)
	}
	detail := bytes.TrimSpace(envelope.Detail)
	if len(detail) == 0 || bytes.Equal(detail, []byte("null")) {
		return "", ""
	}
	switch detail[0] {
	case '{':
		var object struct {
			ErrorType string `json:"error_type"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(detail, &object); err != nil {
			return "", compactJSON(detail)
		}
		return object.ErrorType, object.Message
	case '"':
		var text string
		if err := json.Unmarshal(detail, &text); err != nil {
			return "", compactJSON(detail)
		}
		return "", text
	case '[':
		return "", validationText(detail)
	default:
		return "", compactJSON(detail)
	}
}

// validationText renders a validation failure array as one line per offending
// field, each the field path followed by what is wrong with it.
func validationText(detail json.RawMessage) string {
	var entries []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	// UseNumber keeps an integer path element, which is the index of an
	// offending array entry, exact instead of routing it through float64.
	dec := json.NewDecoder(bytes.NewReader(detail))
	dec.UseNumber()
	if err := dec.Decode(&entries); err != nil {
		return compactJSON(detail)
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		loc := make([]string, 0, len(entry.Loc))
		for _, element := range entry.Loc {
			loc = append(loc, fmt.Sprint(element))
		}
		path := strings.Join(loc, ".")
		switch {
		case path != "" && entry.Msg != "":
			parts = append(parts, path+": "+entry.Msg)
		case entry.Msg != "":
			parts = append(parts, entry.Msg)
		case path != "":
			parts = append(parts, path)
		}
	}
	if len(parts) == 0 {
		return compactJSON(detail)
	}
	return strings.Join(parts, "; ")
}

// compactJSON renders JSON the message shapes do not cover as one line, so an
// unrecognised body still reads in an error string.
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return plainText(raw)
	}
	return plainText(buf.Bytes())
}

// maxMessage caps the length of a message lifted out of a response body.
const maxMessage = 512

// plainText trims a body down to something an error string can carry.
func plainText(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > maxMessage {
		return text[:maxMessage] + "..."
	}
	return text
}

// ValidationError is a request the client rejected before sending it. Field
// names what is wrong, as a path into the request: "state", "questions", or
// "questions[id].criteria" for a fault inside one question.
type ValidationError struct {
	// Field is the path of the offending field within the request.
	Field string
	// Message says what the field must hold.
	Message string
}

// Error renders the field path and what it must hold.
func (e *ValidationError) Error() string {
	return "typesafe: invalid request: " + e.Field + " " + e.Message
}
