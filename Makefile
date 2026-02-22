VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LATEST_TAG := $(shell git tag --list 'v*' --sort=-v:refname | head -n1)
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test lint clean all prepare

all: lint test build

build: clean
	go build $(LDFLAGS) -o dist/sc ./cmd/sc/

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf dist

prepare: all
	@echo ""; \
	echo "========== Generating changelog and commit message =========="; \
	echo ""; \
	LAST_TAG="$(LATEST_TAG)"; \
	if [ -z "$$LAST_TAG" ]; then \
		LAST_TAG="$$(git rev-list --max-parents=0 HEAD)"; \
	fi; \
	RANGE="$$LAST_TAG..HEAD"; \
	COMMITS="$$(git log --oneline $$RANGE)"; \
	DIFF_STAT="$$(git diff --stat $$LAST_TAG 2>/dev/null || git diff --stat)"; \
	UNSTAGED_DIFF="$$(git diff)"; \
	STAGED_DIFF="$$(git diff --cached)"; \
	UNTRACKED="$$(git ls-files --others --exclude-standard)"; \
	if [ -z "$$COMMITS" ] && [ -z "$$UNSTAGED_DIFF" ] && [ -z "$$STAGED_DIFF" ] && [ -z "$$UNTRACKED" ]; then \
		echo "Nothing to prepare — no commits since $$LAST_TAG and no local changes."; \
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
$${COMMITS:-none} \
\
Diff stats: \
$$DIFF_STAT \
\
Unstaged diff: \
$${UNSTAGED_DIFF:-none} \
\
Staged diff: \
$${STAGED_DIFF:-none} \
\
Untracked files: \
$${UNTRACKED:-none} \
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
---COMMIT_MSG--- \
(A conventional commit message. First line: type(scope): description under 72 chars. \
Then a blank line, then a body with bullet points summarizing key changes. \
Types: feat, fix, chore, refactor, docs, test, ci. Pick the most appropriate.) \
---END_COMMIT_MSG--- \
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
	COMMIT_MSG=$$(sed -n '/^---COMMIT_MSG---$$/,/^---END_COMMIT_MSG---$$/{//d;p;}' /tmp/sc-prepare-output.txt); \
	echo ""; \
	echo "========== CHANGELOG.md updated =========="; \
	echo ""; \
	echo "========== Commit message =========="; \
	echo "$$COMMIT_MSG"; \
	echo ""; \
	git add -A; \
	git commit -m "$$COMMIT_MSG"; \
	echo ""; \
	echo "========== Committed =========="; \
	echo ""; \
	echo "========== PR Title =========="; \
	sed -n '/^---PR_TITLE---$$/,/^---END_PR_TITLE---$$/{//d;p;}' /tmp/sc-prepare-output.txt; \
	echo ""; \
	echo "========== PR Description =========="; \
	sed -n '/^---PR_BODY---$$/,/^---END_PR_BODY---$$/{//d;p;}' /tmp/sc-prepare-output.txt; \
	rm -f /tmp/sc-prepare-output.txt; \
	echo ""; \
	echo "========== Done =========="; \
	echo "Push your branch and open a PR with the title and description above."
