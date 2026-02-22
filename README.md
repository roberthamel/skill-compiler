# sc — Skill Compiler

`sc` compiles interface specifications and human-authored instructions into [Agent Skills](https://agentskills.dev) spec-compliant skill directories and `llms.txt` documentation.

Given a `COMPILER_INSTRUCTIONS.md` file (YAML frontmatter + markdown body) and one or more spec sources (OpenAPI, CLI binary, codebase), it produces:

- A skill directory (`SKILL.md`, `references/`, `scripts/`)
- `llms.txt`, `llms-api.txt`, `llms-full.txt`
- `CHANGELOG.md`

## Installation

**Quick install:**

```sh
curl -fsSL https://raw.githubusercontent.com/roberthamel/skill-compiler/main/install.sh | bash
```

**From source (requires Go 1.22+):**

```sh
go install github.com/roberthamel/skill-compiler/cmd/sc@latest
```

**Build locally:**

```sh
git clone https://github.com/roberthamel/skill-compiler.git
cd skill-compiler
make build
# Binary is at dist/sc
```

## Quickstart

```sh
# 1. Scaffold instructions from an OpenAPI spec
sc init --name my-api --spec ./openapi.yaml

# 2. Review and edit COMPILER_INSTRUCTIONS.md
#    Add Product, Workflows, Examples, and Common patterns sections

# 3. Generate skill artifacts
sc generate

# 4. Preview locally
sc serve
# Open http://localhost:4321/llms.txt
```

## Configuration

`sc` resolves configuration in this priority order (highest wins):

1. CLI flags (`--provider`, `--model`)
2. Frontmatter in `COMPILER_INSTRUCTIONS.md` (`provider:` block)
3. Environment variables (`SC_PROVIDER`, `SC_MODEL`, `SC_API_KEY`, `SC_BASE_URL`)
4. Config file (`~/.config/sc/config.yaml`)

**Config keys:**

| Key        | Description                          | Env var        |
|------------|--------------------------------------|----------------|
| `provider` | LLM provider (`anthropic`, `openai`) | `SC_PROVIDER`  |
| `model`    | Model name                           | `SC_MODEL`     |
| `api-key`  | API key                              | `SC_API_KEY`   |
| `base-url` | Custom API base URL                  | `SC_BASE_URL`  |

**Managing config:**

```sh
sc config set provider anthropic
sc config set api-key sk-ant-...
sc config list
sc config reset
```

## Architecture

```
cmd/sc/                  CLI entry point (cobra commands)
internal/
  instructions/          Parse COMPILER_INSTRUCTIONS.md (frontmatter + sections)
  plugins/
    openapi/             OpenAPI 3.x spec → IR
    cli/                 CLI help text → IR (BFS crawl)
    codebase/            File tree + package manifests → IR
  ir/                    Intermediate Representation + plugin registry
  generate/              Artifact generation pipeline + prompts
  provider/              LLM provider abstraction (Anthropic, OpenAI)
  cache/                 SHA-256 input/output hashing + lockfile
  config/                Config file + env var + flag resolution
```

**Data flow:**

```
COMPILER_INSTRUCTIONS.md
        │
        ▼
  instructions.Parse()
        │
        ├── spec sources ──▶ plugins ──▶ IR (operations, types, auth)
        │
        ▼
  generate.Pipeline
        │
        ├── per-artifact system prompt + relevant sections
        ├── cache check (skip if inputs unchanged)
        ├── LLM call (provider.Generate)
        │
        ▼
  Artifacts: SKILL.md, references/, llms.txt, CHANGELOG.md, scripts/
```

## Development

```sh
# Build
make build

# Run tests
make test

# Run linter
make lint

# Prepare a release (generates CHANGELOG.md, PR title/description)
make prepare

# Clean build artifacts
make clean
```

**Release workflow:** All changes go through PRs to `main`. When a PR is merged, CI auto-tags a minor version bump (e.g., `v0.1.0` → `v0.2.0`), which triggers GoReleaser to build cross-platform binaries and create a GitHub release.

Before opening a PR, run `make prepare` to generate the changelog entry and get a suggested PR title and description. This requires the [Claude CLI](https://claude.ai/claude-code).
