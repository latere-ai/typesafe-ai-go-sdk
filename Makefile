# SPDX-FileCopyrightText: 2026 Latere AI
# SPDX-License-Identifier: Apache-2.0

.PHONY: check test race cover lint fmt hooks release

# The whole shared bar. Every gate lives in lateregate, pinned as a tool in
# go.mod. The plan: `go tool lateregate list`. One gate: `go tool lateregate <gate>`.
check:
	@go tool lateregate

test:
	@go tool lateregate test

race:
	@go tool lateregate race

cover:
	go test -coverprofile=coverage.out -covermode=atomic -coverpkg=./... ./...
	@go tool lateregate cover -profile=coverage.out

lint:
	@go tool lateregate lint

fmt:
	gofmt -w .

# hooks installs the repository git hooks; both delegate to lateregate.
hooks:
	git config core.hooksPath .githooks

# release cuts a tag from CHANGELOG.md: the notes under "Unreleased" become
# the section for VERSION, then commit, tag, and push.
release:
	@go tool lateregate release "$(VERSION)"
