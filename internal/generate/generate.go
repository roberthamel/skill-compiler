package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/roberthamel/skill-compiler/internal/instructions"
	"github.com/roberthamel/skill-compiler/internal/ir"
	"github.com/roberthamel/skill-compiler/internal/provider"
)

// ArtifactID identifies an artifact type.
type ArtifactID string

const (
	ArtifactSkill     ArtifactID = "skill"
	ArtifactReference ArtifactID = "reference"
	ArtifactExamples  ArtifactID = "examples"
	ArtifactScripts   ArtifactID = "scripts"
	ArtifactLlms      ArtifactID = "llms"
	ArtifactLlmsAPI   ArtifactID = "llms-api"
	ArtifactLlmsFull  ArtifactID = "llms-full"
	ArtifactChangelog ArtifactID = "changelog"
)

// AllArtifacts lists all artifact IDs in generation order.
var AllArtifacts = []ArtifactID{
	ArtifactSkill, ArtifactReference, ArtifactExamples, ArtifactScripts,
	ArtifactLlms, ArtifactLlmsAPI, ArtifactLlmsFull, ArtifactChangelog,
}

// ArtifactResult holds the output of generating a single artifact.
type ArtifactResult struct {
	ID       ArtifactID
	Content  string
	FilePath string // relative to output dir
	Response *provider.GenerateResponse
	Err      error
}

// Options controls artifact generation.
type Options struct {
	OutputDir      string
	Only           []string // generate only these artifact IDs
	Force          bool
	DryRun         bool
	Diff           bool
	Verbose        bool
	PrevArtifacts  map[ArtifactID]string   // previous artifact contents for changelog
	SkipArtifacts  map[ArtifactID]bool     // per-artifact cache hits to skip
}

// Pipeline generates all artifacts from IR and instructions.
type Pipeline struct {
	Provider provider.Provider
	IR       *ir.IntermediateRepr
	Inst     *instructions.Instructions
	Opts     Options
}

// Run executes the generation pipeline.
func (p *Pipeline) Run(ctx context.Context) ([]ArtifactResult, error) {
	artifacts := p.enabledArtifacts()

	// Separate changelog (depends on all others) from parallel artifacts
	var parallel []ArtifactID
	hasChangelog := false
	for _, id := range artifacts {
		if id == ArtifactChangelog {
			hasChangelog = true
		} else {
			parallel = append(parallel, id)
		}
	}

	// Generate parallel artifacts concurrently
	var mu sync.Mutex
	var results []ArtifactResult
	var wg sync.WaitGroup

	for _, id := range parallel {
		wg.Add(1)
		go func(id ArtifactID) {
			defer wg.Done()
			result := p.generateArtifact(ctx, id)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	// Check for errors in parallel generation
	for _, r := range results {
		if r.Err != nil {
			return results, fmt.Errorf("generating %s: %w", r.ID, r.Err)
		}
	}

	// Generate changelog after all others
	if hasChangelog {
		result := p.generateArtifact(ctx, ArtifactChangelog)
		results = append(results, result)
		if result.Err != nil {
			return results, fmt.Errorf("generating changelog: %w", result.Err)
		}
	}

	return results, nil
}

func (p *Pipeline) enabledArtifacts() []ArtifactID {
	if len(p.Opts.Only) > 0 {
		onlySet := make(map[string]bool)
		for _, o := range p.Opts.Only {
			onlySet[strings.ToLower(strings.TrimSpace(o))] = true
		}
		var filtered []ArtifactID
		for _, id := range AllArtifacts {
			if onlySet[string(id)] {
				filtered = append(filtered, id)
			}
		}
		return filtered
	}

	var filtered []ArtifactID
	for _, id := range AllArtifacts {
		artifactName := string(id)
		if toggle, ok := p.Inst.Frontmatter.Artifacts[artifactName]; ok {
			if !toggle.IsEnabled() {
				continue
			}
		}
		filtered = append(filtered, id)
	}
	return filtered
}

func (p *Pipeline) generateArtifact(ctx context.Context, id ArtifactID) ArtifactResult {
	systemPrompt := p.systemPrompt(id)
	userMessage := p.userMessage(id)
	filePath := p.artifactPath(id)

	if p.Opts.DryRun {
		tokens := estimateTokens(systemPrompt + userMessage)
		return ArtifactResult{
			ID:       id,
			FilePath: filePath,
			Content:  fmt.Sprintf("[dry-run] Would generate %s (~%d input tokens)", id, tokens),
		}
	}

	// Skip if cache says this artifact is up to date
	if p.Opts.SkipArtifacts[id] {
		fmt.Printf("  Skipping %s (cached)\n", id)
		return ArtifactResult{ID: id, FilePath: filePath}
	}

	fmt.Printf("  Generating %s...\n", id)

	if p.Opts.Verbose {
		fmt.Printf("  [verbose] %s system prompt: %d chars\n", id, len(systemPrompt))
		fmt.Printf("  [verbose] %s user message: %d chars\n", id, len(userMessage))
	}

	start := time.Now()
	resp, err := p.Provider.Generate(ctx, provider.GenerateRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		MaxTokens:    maxTokensForArtifact(id),
	})
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("  FAILED %s: %s\n", id, err)
		return ArtifactResult{ID: id, FilePath: filePath, Err: err}
	}

	if p.Opts.Verbose && resp != nil {
		fmt.Printf("  [verbose] %s: %d in / %d out tokens, %s\n", id, resp.TokensIn, resp.TokensOut, elapsed.Round(time.Millisecond))
	}

	fmt.Printf("  Done %s (%s)\n", id, elapsed.Round(time.Millisecond))

	return ArtifactResult{
		ID:       id,
		Content:  resp.Content,
		FilePath: filePath,
		Response: resp,
	}
}

// SystemPromptFor returns the system prompt for a given artifact ID (exported for cache hashing).
func (p *Pipeline) SystemPromptFor(id ArtifactID) string {
	return p.systemPrompt(id)
}

// RelevantSections returns the instruction sections relevant to a given artifact,
// concatenated as a single string for cache hashing.
func (p *Pipeline) RelevantSections(id ArtifactID) string {
	var parts []string
	switch id {
	case ArtifactSkill, ArtifactLlmsFull, ArtifactScripts:
		for name, content := range p.Inst.Sections {
			parts = append(parts, name+"\n"+content)
		}
	case ArtifactExamples:
		for _, key := range []string{"Workflows", "Examples", "Common patterns"} {
			if content, ok := p.Inst.Sections[key]; ok {
				parts = append(parts, key+"\n"+content)
			}
		}
	case ArtifactLlms:
		if content, ok := p.Inst.Sections["Product"]; ok {
			parts = append(parts, "Product\n"+content)
		}
	case ArtifactChangelog:
		// Changelog depends on previous artifacts, not instruction sections
	default:
		// reference, llms-api: no specific sections
	}
	return strings.Join(parts, "\n\n")
}

// ArtifactPath returns the relative file path for a given artifact ID.
func (p *Pipeline) ArtifactPath(id ArtifactID) string {
	return p.artifactPath(id)
}

func (p *Pipeline) systemPrompt(id ArtifactID) string {
	switch id {
	case ArtifactSkill:
		return SkillPrompt
	case ArtifactReference:
		return ReferencePrompt
	case ArtifactExamples:
		return ExamplesPrompt
	case ArtifactScripts:
		return ScriptsPrompt
	case ArtifactLlms:
		return LlmsTxtPrompt
	case ArtifactLlmsAPI:
		return LlmsAPITxtPrompt
	case ArtifactLlmsFull:
		return LlmsFullTxtPrompt
	case ArtifactChangelog:
		return ChangelogPrompt
	default:
		return ""
	}
}

func (p *Pipeline) userMessage(id ArtifactID) string {
	irJSON, _ := json.MarshalIndent(p.IR, "", "  ")
	name := p.Inst.Frontmatter.Name
	envPrefix := p.Inst.EnvPrefix()

	var parts []string
	parts = append(parts, fmt.Sprintf("Tool/Project Name: %s", name))
	parts = append(parts, fmt.Sprintf("Environment Variable Prefix: %s", envPrefix))

	// Add env vars from skill config
	if len(p.Inst.Frontmatter.Skill.Env) > 0 {
		parts = append(parts, fmt.Sprintf("Environment Variables: %s", strings.Join(p.Inst.Frontmatter.Skill.Env, ", ")))
	}

	// Add skill metadata for SKILL.md
	if id == ArtifactSkill {
		if p.Inst.Frontmatter.Skill.License != "" {
			parts = append(parts, fmt.Sprintf("License: %s", p.Inst.Frontmatter.Skill.License))
		}
		if p.Inst.Frontmatter.Skill.Compatibility != "" {
			parts = append(parts, fmt.Sprintf("Compatibility: %s", p.Inst.Frontmatter.Skill.Compatibility))
		}
		if p.Inst.Frontmatter.Skill.AllowedTools != "" {
			parts = append(parts, fmt.Sprintf("Allowed Tools: %s", p.Inst.Frontmatter.Skill.AllowedTools))
		}
		if len(p.Inst.Frontmatter.Skill.Metadata) > 0 {
			metaJSON, _ := json.Marshal(p.Inst.Frontmatter.Skill.Metadata)
			parts = append(parts, fmt.Sprintf("Metadata: %s", string(metaJSON)))
		}
	}

	// Add relevant instructions sections based on artifact type
	switch id {
	case ArtifactSkill:
		for name, content := range p.Inst.Sections {
			parts = append(parts, fmt.Sprintf("## Instructions: %s\n%s", name, content))
		}
	case ArtifactExamples:
		for _, key := range []string{"Workflows", "Examples", "Common patterns"} {
			if content, ok := p.Inst.Sections[key]; ok {
				parts = append(parts, fmt.Sprintf("## Instructions: %s\n%s", key, content))
			}
		}
	case ArtifactLlms:
		if content, ok := p.Inst.Sections["Product"]; ok {
			parts = append(parts, fmt.Sprintf("## Instructions: Product\n%s", content))
		}
	case ArtifactLlmsFull:
		for name, content := range p.Inst.Sections {
			parts = append(parts, fmt.Sprintf("## Instructions: %s\n%s", name, content))
		}
	case ArtifactScripts:
		for name, content := range p.Inst.Sections {
			parts = append(parts, fmt.Sprintf("## Instructions: %s\n%s", name, content))
		}
	case ArtifactChangelog:
		hasPrev := false
		for _, prevID := range []ArtifactID{ArtifactSkill, ArtifactReference, ArtifactExamples} {
			if prev, ok := p.Opts.PrevArtifacts[prevID]; ok && prev != "" {
				parts = append(parts, fmt.Sprintf("## Previous %s\n%s", prevID, prev))
				hasPrev = true
			}
		}
		if prev, ok := p.Opts.PrevArtifacts[ArtifactChangelog]; ok && prev != "" {
			parts = append(parts, fmt.Sprintf("## Previous CHANGELOG.md\n%s", prev))
			hasPrev = true
		}
		if !hasPrev {
			parts = append(parts, "## Note\nThis is the first generation — no previous artifacts exist.")
		}
	}

	parts = append(parts, fmt.Sprintf("## Spec (Intermediate Representation)\n```json\n%s\n```", string(irJSON)))

	return strings.Join(parts, "\n\n")
}

func (p *Pipeline) artifactPath(id ArtifactID) string {
	name := p.Inst.Frontmatter.Name
	artifactKey := string(id)

	// Check for custom filename
	if toggle, ok := p.Inst.Frontmatter.Artifacts[artifactKey]; ok && toggle.Filename != "" {
		switch id {
		case ArtifactSkill:
			return filepath.Join(name, toggle.Filename)
		case ArtifactReference, ArtifactExamples:
			return filepath.Join(name, "references", toggle.Filename)
		case ArtifactScripts:
			return filepath.Join(name, "scripts", toggle.Filename)
		default:
			return toggle.Filename
		}
	}

	switch id {
	case ArtifactSkill:
		return filepath.Join(name, "SKILL.md")
	case ArtifactReference:
		return filepath.Join(name, "references", "reference.md")
	case ArtifactExamples:
		return filepath.Join(name, "references", "examples.md")
	case ArtifactScripts:
		return filepath.Join(name, "scripts") // directory; scripts parsed from content
	case ArtifactLlms:
		return "llms.txt"
	case ArtifactLlmsAPI:
		return "llms-api.txt"
	case ArtifactLlmsFull:
		return "llms-full.txt"
	case ArtifactChangelog:
		return "CHANGELOG.md"
	default:
		return string(id) + ".txt"
	}
}

// WriteResults writes all generated artifacts to the output directory.
func WriteResults(outputDir string, results []ArtifactResult) error {
	for _, r := range results {
		if r.Err != nil || r.Content == "" {
			continue
		}

		if r.ID == ArtifactScripts {
			// Parse scripts from content and write each one
			if err := writeScripts(outputDir, r.FilePath, r.Content); err != nil {
				return fmt.Errorf("writing scripts: %w", err)
			}
			continue
		}

		fullPath := filepath.Join(outputDir, r.FilePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", r.FilePath, err)
		}
		if err := os.WriteFile(fullPath, []byte(r.Content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", r.FilePath, err)
		}
	}
	return nil
}

// writeScripts parses code blocks from LLM output and writes each as a file.
func writeScripts(outputDir, scriptsDir, content string) error {
	dir := filepath.Join(outputDir, scriptsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Parse code blocks: ```filename\n...\n```
	lines := strings.Split(content, "\n")
	var currentFile string
	var currentContent []string
	inBlock := false

	for _, line := range lines {
		if strings.HasPrefix(line, "```") && !inBlock {
			currentFile = strings.TrimPrefix(line, "```")
			currentFile = strings.TrimSpace(currentFile)
			currentContent = nil
			inBlock = true
		} else if line == "```" && inBlock {
			if currentFile != "" {
				path := filepath.Join(dir, currentFile)
				data := strings.Join(currentContent, "\n") + "\n"
				if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
					return fmt.Errorf("writing script %s: %w", currentFile, err)
				}
			}
			inBlock = false
			currentFile = ""
		} else if inBlock {
			currentContent = append(currentContent, line)
		}
	}

	return nil
}

func maxTokensForArtifact(id ArtifactID) int {
	switch id {
	case ArtifactSkill:
		return 8192
	case ArtifactReference:
		return 16384
	case ArtifactExamples:
		return 8192
	case ArtifactScripts:
		return 8192
	case ArtifactLlms:
		return 1024
	case ArtifactLlmsAPI:
		return 4096
	case ArtifactLlmsFull:
		return 16384
	case ArtifactChangelog:
		return 4096
	default:
		return 8192
	}
}

func estimateTokens(text string) int {
	// Rough estimate: ~4 chars per token
	return len(text) / 4
}
