// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: Apache-2.0

package typesafe

// Version is the version of this client. It is the version part of the
// User-Agent header every request carries, and it moves with the tag the
// module is released under.
const Version = "0.1.0"

// defaultUserAgent is the User-Agent header value a client sends unless
// WithUserAgent replaces it.
const defaultUserAgent = "typesafe-ai-go-sdk/" + Version
