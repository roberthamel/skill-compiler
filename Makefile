VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LATEST_TAG := $(shell git tag --list 'v*' --sort=-v:refname | head -n1)
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test lint clean prepare

build: clean
	go build $(LDFLAGS) -o dist/sc ./cmd/sc/

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf dist

prepare:
	@LAST_TAG="$(LATEST_TAG)"; \
	if [ -z "$$LAST_TAG" ]; then \
		LAST_TAG="$$(git rev-list --max-parents=0 HEAD)"; \
		RANGE="$$LAST_TAG..HEAD"; \
	else \
		RANGE="$$LAST_TAG..HEAD"; \
	fi; \
	COMMITS="$$(git log --oneline $$RANGE)"; \
	DIFF_STAT="$$(git diff --stat $$LAST_TAG..HEAD 2>/dev/null || git diff --stat $$RANGE)"; \
	if [ -z "$$COMMITS" ]; then \
		echo "No commits since $$LAST_TAG — nothing to prepare."; \
		exit 0; \
	fi; \
	EXISTING_CHANGELOG=""; \
	if [ -f CHANGELOG.md ]; then \
		EXISTING_CHANGELOG="$$(cat CHANGELOG.md)"; \
	fi; \
	claude --print -p "You are preparing a release for the sc (skill-compiler) CLI tool. \
The latest tag is: $${LAST_TAG:-none} \
\
Commits since last tag: \
$$COMMITS \
\
Diff stats: \
$$DIFF_STAT \
\
Existing CHANGELOG.md: \
$$EXISTING_CHANGELOG \
\
Output EXACTLY this format with no other text: \
\
---CHANGELOG--- \
(A complete updated CHANGELOG.md file. Use keep-a-changelog format: https://keepachangelog.com. \
Group changes under: Added, Changed, Fixed, Removed as appropriate. \
Each version section header: ## [vX.Y.Z] - YYYY-MM-DD. \
Prepend the new entry to existing content. Use today's date. \
Bump minor version from the latest tag for the new version number. \
If no previous tag, start at v0.1.0.) \
---END_CHANGELOG--- \
\
---PR_TITLE--- \
(A concise PR title under 70 chars summarizing the changes) \
---END_PR_TITLE--- \
\
---PR_BODY--- \
(A PR description in markdown with a ## Summary section of 2-5 bullet points \
and a ## Changes section listing key modifications) \
---END_PR_BODY---" > /tmp/sc-prepare-output.txt; \
	sed -n '/^---CHANGELOG---$$/,/^---END_CHANGELOG---$$/{//d;p;}' /tmp/sc-prepare-output.txt > CHANGELOG.md; \
	echo ""; \
	echo "========== CHANGELOG.md updated =========="; \
	echo ""; \
	echo "========== PR Title =========="; \
	sed -n '/^---PR_TITLE---$$/,/^---END_PR_TITLE---$$/{//d;p;}' /tmp/sc-prepare-output.txt; \
	echo ""; \
	echo "========== PR Description =========="; \
	sed -n '/^---PR_BODY---$$/,/^---END_PR_BODY---$$/{//d;p;}' /tmp/sc-prepare-output.txt; \
	rm -f /tmp/sc-prepare-output.txt
