// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Model is one model or alias an account may name in Request.Model.
type Model struct {
	// Name is the model id or alias, such as "jev-1.13.0" or "jev-latest".
	Name string `json:"name"`
	// Description says what the model is for.
	Description string `json:"description"`
	// ReleaseDate is when the model or alias was released. The API sends it
	// as an RFC 3339 timestamp; a value in any other format fails the decode
	// of the whole listing.
	ReleaseDate time.Time `json:"release_date"`
}

// ListModels returns the models the account may name in Request.Model. The
// list holds the aliases as well as the versioned ids, and a versioned id is
// accepted by Request.Model whether or not it appears here.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	result, err := c.do(ctx, http.MethodGet, pathModels, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(result.body, &body); err != nil {
		return nil, fmt.Errorf("typesafe: decoding the %s response: %w", pathModels, err)
	}
	return body.Models, nil
}
