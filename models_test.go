// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModels(t *testing.T) {
	clearEnv(t)
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotType   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		_, _ = w.Write(fixture(t, "response_models.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	models, err := c.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != pathModels {
		t.Errorf("sent %s %s, want GET %s", gotMethod, gotPath, pathModels)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// A GET carries no body, so it must not claim a content type.
	if gotType != "" {
		t.Errorf("Content-Type = %q, want none on a GET", gotType)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].Name != "jev-latest" {
		t.Errorf("Name = %q", models[0].Name)
	}
	if models[0].Description == "" {
		t.Error("Description is empty")
	}
	want := time.Date(2026, 9, 10, 18, 38, 1, 391457000, time.UTC)
	if !models[0].ReleaseDate.Equal(want) {
		t.Errorf("ReleaseDate = %v, want %v", models[0].ReleaseDate, want)
	}
}

func TestListModelsReportsAnUndecodableResponse(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"x","release_date":"last tuesday"}]}`))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.ListModels(t.Context())
	if err == nil || !strings.Contains(err.Error(), "decoding the "+pathModels+" response") {
		t.Fatalf("ListModels = %v, want a decoding failure", err)
	}
}

func TestListModelsReturnsAnAPIError(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(requestIDHeader, "req_nope")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(fixture(t, "error_forbidden.json"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.ListModels(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ListModels = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.RequestID != "req_nope" {
		t.Errorf("error lost the response detail: %+v", apiErr)
	}
}
