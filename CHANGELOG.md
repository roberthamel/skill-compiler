# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com),
and this project adheres to [Semantic Versioning](https://semver.org).

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

## [v0.1.0] - 2025-01-01

Initial release.
