# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com),
and this project adheres to [Semantic Versioning](https://semver.org).

## [v1.2.0] - 2026-02-22

### Fixed
- Fixed version detection regex in CI release workflow for more reliable tag parsing
- Improved release notes fallback mechanism when changelog entry is missing

### Changed
- Skip non-releasable commits in CI release workflow to avoid unnecessary version bumps
- Updated install.sh to reference new repository location (`rhcloud-dev/skill-compiler`)

## [v1.1.0] - 2026-02-22

### Changed
- Skip non-releasable commits in CI release workflow to avoid unnecessary version bumps
- Improved release notes fallback when no changelog entry exists for a version
- Fixed SKIP_SCOPE_RE regex to use POSIX character class and support breaking change indicator (`!`) before colon

## [v0.6.0] - 2026-02-22

### Added
- Apache 2.0 LICENSE file
- License section in README

### Changed
- CI release job now scans all commits since last tag to determine semver bump type, instead of only the latest commit
- Added skip logic for non-releasable commit types and scopes (ci, chore, docs, style, test, build) in version bump detection
- Improved release notes generation with git log fallback when CHANGELOG entry is missing
- Improved release notes extraction with more robust `awk` parsing in CI workflow
- Updated goreleaser archive configuration to use `formats` list syntax
- Restricted CI tag and release jobs to run only on the upstream repository
- Enhanced `make prepare` to show full diffs instead of just changed file names
- Clarified commit message and PR description prompt scope in Makefile
- Tightened BREAKING CHANGE detection regex to require colon suffix and support hyphen variant

## [v0.5.0] - 2026-02-22

### Changed
- Replaced `sed` with `awk` for more robust release notes extraction in CI workflow
- Updated goreleaser archive config from `format` to `formats` list syntax

## [v0.4.0] - 2026-02-22

### Changed
- Moved release notes extraction from goreleaser hook to CI workflow step
- Simplified goreleaser configuration by removing inline release notes generation
- Pass release notes to goreleaser via `--release-notes` CLI flag instead of `release` config block

## [v0.3.0] - 2026-02-22

### Changed
- Consolidated release process into main CI workflow configuration
- Removed redundant release.yaml workflow file

## [v0.2.0] - 2026-02-22

### Added
- Comprehensive test suite for generate, instructions, config, cache, CLI plugins, codebase, OpenAPI, provider, and main packages
- Auto-tagging workflow for automated version management
- Changelog generation support
- Release notes generation in goreleaser configuration
- README documentation
- golangci-lint configuration

### Changed
- Updated module path and import statements to reflect new repository structure
- Enhanced CI workflows with improved configuration
- Refactored build process and config handling
- Updated Go version and golangci-lint version in CI
- Improved error handling in provider and plugin packages
- Enhanced OpenAPI plugin with better parsing and validation
- Enhanced goreleaser configuration with release notes generation

### Fixed
- Formatting of golangci-lint version in CI configuration

## [v0.1.0] - 2026-02-22

Initial release.
