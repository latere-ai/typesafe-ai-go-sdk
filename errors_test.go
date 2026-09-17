// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads one file from testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

func TestAPIErrorError(t *testing.T) {
	tests := []struct {
		name string
		err  APIError
		want string
	}{
		{
			name: "everything",
			err:  APIError{StatusCode: 401, Type: "authentication_error", Message: "bad key", RequestID: "req_1"},
			want: "typesafe: http 401 authentication_error: bad key (request id req_1)",
		},
		{
			name: "no type",
			err:  APIError{StatusCode: 404, Message: "Not Found"},
			want: "typesafe: http 404: Not Found",
		},
		{
			name: "status only",
			err:  APIError{StatusCode: 500},
			want: "typesafe: http 500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIErrorTemporary(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{statusOverloaded, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusUnprocessableEntity, false},
		{http.StatusOK, false},
	}
	for _, tt := range tests {
		err := &APIError{StatusCode: tt.status}
		if got := err.Temporary(); got != tt.want {
			t.Errorf("Temporary() for %d = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestNewAPIErrorFixtures(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		status      int
		wantType    string
		wantMessage string
	}{
		{
			name:        "unauthorized object envelope",
			file:        "error_unauthorized.json",
			status:      http.StatusUnauthorized,
			wantType:    "authentication_error",
			wantMessage: "Cannot authenticate with the server. Please check your API key and try again.",
		},
		{
			name:        "forbidden object envelope",
			file:        "error_forbidden.json",
			status:      http.StatusForbidden,
			wantType:    "authentication_error",
			wantMessage: "Must supply an API key! Check your request and try again.",
		},
		{
			name:        "not found bare string",
			file:        "error_not_found.json",
			status:      http.StatusNotFound,
			wantMessage: "Not Found",
		},
		{
			name:        "method not allowed bare string",
			file:        "error_method_not_allowed.json",
			status:      http.StatusMethodNotAllowed,
			wantMessage: "Method Not Allowed",
		},
		{
			name:        "bad request object envelope",
			file:        "error_bad_request_object.json",
			status:      http.StatusBadRequest,
			wantType:    "api_usage_error",
			wantMessage: "Unknown model: nope",
		},
		{
			name:        "bad request bare string",
			file:        "error_bad_request_string.json",
			status:      http.StatusBadRequest,
			wantMessage: "Noul question must have criteria or instructions: q",
		},
		{
			name:        "validation array",
			file:        "error_validation.json",
			status:      http.StatusUnprocessableEntity,
			wantMessage: "body.state: Field required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fixture(t, tt.file)
			err := newAPIError(tt.status, "req_abc", body)
			if err.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", err.StatusCode, tt.status)
			}
			if err.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", err.Type, tt.wantType)
			}
			if err.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", err.Message, tt.wantMessage)
			}
			if err.RequestID != "req_abc" {
				t.Errorf("RequestID = %q, want req_abc", err.RequestID)
			}
			if string(err.Body) != string(body) {
				t.Error("Body does not hold the response bytes as they arrived")
			}
		})
	}
}

func TestParseErrorBodyShapes(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantType    string
		wantMessage string
	}{
		{
			name:        "not json at all",
			body:        "  <html>502 Bad Gateway</html>\n",
			wantMessage: "<html>502 Bad Gateway</html>",
		},
		{name: "empty body"},
		{name: "no detail field", body: `{"error":"nope"}`},
		{name: "null detail", body: `{"detail":null}`},
		{
			name:        "detail is a number",
			body:        `{"detail": 42}`,
			wantMessage: "42",
		},
		{
			name:        "detail object with unexpected field types",
			body:        `{"detail":{"error_type":7}}`,
			wantMessage: `{"error_type":7}`,
		},
		{
			name:        "detail array the validation shape does not fit",
			body:        `{"detail":[1, 2]}`,
			wantMessage: "[1,2]",
		},
		{
			name:        "detail array of empty entries",
			body:        `{"detail":[{}]}`,
			wantMessage: `[{}]`,
		},
		{
			name:        "validation entry with a numeric path element",
			body:        `{"detail":[{"loc":["body","questions",0],"msg":"bad"}]}`,
			wantMessage: "body.questions.0: bad",
		},
		{
			name:        "validation entry with no path",
			body:        `{"detail":[{"msg":"bad"}]}`,
			wantMessage: "bad",
		},
		{
			name:        "validation entry with no message",
			body:        `{"detail":[{"loc":["body"]}]}`,
			wantMessage: "body",
		},
		{
			name:        "two validation entries",
			body:        `{"detail":[{"loc":["body","state"],"msg":"Field required"},{"loc":["body","model"],"msg":"Field required"}]}`,
			wantMessage: "body.state: Field required; body.model: Field required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotMessage := parseErrorBody([]byte(tt.body))
			if gotType != tt.wantType {
				t.Errorf("type = %q, want %q", gotType, tt.wantType)
			}
			if gotMessage != tt.wantMessage {
				t.Errorf("message = %q, want %q", gotMessage, tt.wantMessage)
			}
		})
	}
}

func TestParseErrorBodyKeepsLargeIntegerPathElements(t *testing.T) {
	body := `{"detail":[{"loc":["body",9007199254740993],"msg":"bad"}]}`
	_, message := parseErrorBody([]byte(body))
	if message != "body.9007199254740993: bad" {
		t.Errorf("message = %q, want the index unrounded", message)
	}
}

func TestNewAPIErrorFallsBackToTheStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusTooManyRequests, "Too Many Requests"},
		{statusOverloaded, "Overloaded"},
		{299, "unexpected status"},
	}
	for _, tt := range tests {
		err := newAPIError(tt.status, "", nil)
		if err.Message != tt.want {
			t.Errorf("Message for %d = %q, want %q", tt.status, err.Message, tt.want)
		}
	}
}

func TestCompactJSONFallsBackToText(t *testing.T) {
	if got := compactJSON([]byte("{not json")); got != "{not json" {
		t.Errorf("compactJSON = %q, want the raw text", got)
	}
}

func TestPlainTextTruncates(t *testing.T) {
	got := plainText([]byte(strings.Repeat("x", maxMessage+10)))
	if len(got) != maxMessage+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("plainText returned %d bytes, want %d with an ellipsis", len(got), maxMessage+3)
	}
}

func TestAPIErrorIsFoundByErrorsAs(t *testing.T) {
	var wrapped error = &APIError{StatusCode: 429}
	var target *APIError
	if !errors.As(wrapped, &target) || target.StatusCode != 429 {
		t.Error("errors.As did not reach the APIError")
	}
}

func TestValidationErrorError(t *testing.T) {
	err := &ValidationError{Field: "questions[a].Criteria", Message: "must hold at least one option"}
	want := "typesafe: invalid request: questions[a].Criteria must hold at least one option"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
