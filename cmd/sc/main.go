package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/roberthamel/skill-compiler/internal/cache"
	"github.com/roberthamel/skill-compiler/internal/config"
	"github.com/roberthamel/skill-compiler/internal/generate"
	"github.com/roberthamel/skill-compiler/internal/instructions"
	"github.com/roberthamel/skill-compiler/internal/ir"
	cliplugin "github.com/roberthamel/skill-compiler/internal/plugins/cli"
	"github.com/roberthamel/skill-compiler/internal/plugins/codebase"
	"github.com/roberthamel/skill-compiler/internal/plugins/openapi"
	"github.com/roberthamel/skill-compiler/internal/provider"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:   "sc",
		Short: "Skill Compiler — compile interface specs into Agent Skills",
		Long: `sc compiles interface specifications and human-authored instructions
into Agent Skills spec-compliant skill directories and llms.txt documentation.

It reads a COMPILER_INSTRUCTIONS.md file (YAML frontmatter + markdown body)
and one or more spec sources (OpenAPI, CLI binary, codebase) to produce:
  - A skill directory (SKILL.md, references/, scripts/)
  - llms.txt, llms-api.txt, llms-full.txt
  - CHANGELOG.md`,
		Version: version,
	}

	rootCmd.AddCommand(
		newGenerateCmd(),
		newInitCmd(),
		newValidateCmd(),
		newDiffCmd(),
		newServeCmd(),
		newConfigCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate skill artifacts from spec and instructions",
		RunE:  runGenerate,
	}
	cmd.Flags().String("spec", "", "Path to spec file (overrides frontmatter)")
	cmd.Flags().String("instructions", "COMPILER_INSTRUCTIONS.md", "Path to instructions file")
	cmd.Flags().String("out", "", "Output directory (overrides frontmatter)")
	cmd.Flags().StringSlice("only", nil, "Generate only these artifacts (comma-separated)")
	cmd.Flags().Bool("force", false, "Bypass cache and regenerate all artifacts")
	cmd.Flags().Bool("dry-run", false, "Show what would be generated without making LLM calls")
	cmd.Flags().Bool("diff", false, "Show diff against existing files instead of overwriting")
	cmd.Flags().Bool("verbose", false, "Show LLM prompts, token usage, and timing")
	cmd.Flags().String("model", "", "LLM model to use (overrides all other config)")
	cmd.Flags().String("provider", "", "LLM provider to use (overrides all other config)")
	return cmd
}

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a COMPILER_INSTRUCTIONS.md from a spec",
		RunE:  runInit,
	}
	cmd.Flags().String("spec", "", "Path to spec file or CLI binary name")
	cmd.Flags().String("type", "", "Spec type: openapi, cli, codebase")
	cmd.Flags().String("name", "", "Project/tool name")
	cmd.Flags().Bool("force", false, "Overwrite existing instructions file")
	return cmd
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate instructions and spec consistency",
		RunE:  runValidate,
	}
}

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare lockfile hashes against current inputs",
		RunE:  runDiff,
	}
	cmd.Flags().String("against", "", "Directory to compare against")
	return cmd
}

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve generated artifacts locally for testing",
		RunE:  runServe,
	}
	cmd.Flags().String("dir", "", "Directory containing generated artifacts")
	cmd.Flags().Int("port", 4321, "Port to serve on")
	return cmd
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
	}
	cmd.AddCommand(
		newConfigSetCmd(),
		newConfigListCmd(),
		newConfigResetCmd(),
	)
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE:  runConfigSet,
	}
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List current configuration",
		RunE:  runConfigList,
	}
}

func newConfigResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Reset configuration to defaults",
		RunE:  runConfigReset,
	}
}

func newPluginRegistry() *ir.Registry {
	reg := ir.NewRegistry()
	reg.Register(openapi.New())
	reg.Register(cliplugin.New())
	reg.Register(codebase.New())
	return reg
}

func runGenerate(cmd *cobra.Command, args []string) error {
	instPath, _ := cmd.Flags().GetString("instructions")
	specFlag, _ := cmd.Flags().GetString("spec")
	outFlag, _ := cmd.Flags().GetString("out")
	only, _ := cmd.Flags().GetStringSlice("only")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	diffMode, _ := cmd.Flags().GetBool("diff")
	verbose, _ := cmd.Flags().GetBool("verbose")
	modelFlag, _ := cmd.Flags().GetString("model")
	providerFlag, _ := cmd.Flags().GetString("provider")

	// Parse instructions
	inst, err := instructions.Parse(instPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no %s found in current directory — run `sc init` to create one", instPath)
		}
		return err
	}

	// Resolve output directory
	outputDir := inst.Frontmatter.Out
	if outFlag != "" {
		outputDir = outFlag
	}

	// Resolve spec sources
	sources, err := inst.ResolveSpecSources()
	if err != nil {
		return fmt.Errorf("resolving spec sources: %w", err)
	}
	if specFlag != "" {
		sources = []instructions.SpecSource{{Path: specFlag}}
	}

	// Resolve provider
	fmProvider := &config.Config{
		Provider: inst.Frontmatter.Provider.Provider,
		Model:    inst.Frontmatter.Provider.Model,
		APIKey:   inst.Frontmatter.Provider.APIKey,
		BaseURL:  inst.Frontmatter.Provider.BaseURL,
	}
	resolved, err := config.Resolve(providerFlag, modelFlag, "", "", fmProvider)
	if err != nil {
		return fmt.Errorf("resolving provider config: %w", err)
	}

	// Process specs through plugin pipeline
	fmt.Println("Parsing spec sources...")
	reg := newPluginRegistry()
	parsedIR, warnings, err := reg.ProcessSources(sources)
	if err != nil {
		return fmt.Errorf("processing specs: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	fmt.Printf("Parsed %d operations, %d types, %d auth schemes\n",
		len(parsedIR.Operations), len(parsedIR.Types), len(parsedIR.Auth))

	// Load previous artifacts for changelog
	prevArtifacts := generate.LoadPreviousArtifacts(outputDir, inst.Frontmatter.Name)

	// Cache check (unless force)
	projectDir, _ := os.Getwd()
	lockFile, _ := cache.LoadLockFile(projectDir)
	irJSON, _ := json.Marshal(parsedIR)
	specContent := string(irJSON)

	// Create provider (unless dry-run)
	var prov provider.Provider
	if !dryRun {
		prov, err = provider.New(resolved)
		if err != nil {
			return err
		}
		fmt.Printf("Using provider: %s (model: %s)\n", prov.Name(), resolved.Model)
	}

	// Build pipeline
	pipeline := &generate.Pipeline{
		Provider: prov,
		IR:       parsedIR,
		Inst:     inst,
		Opts: generate.Options{
			OutputDir:     outputDir,
			Only:          only,
			Force:         force,
			DryRun:        dryRun,
			Diff:          diffMode,
			Verbose:       verbose,
			PrevArtifacts: prevArtifacts,
		},
	}

	// Check cache per artifact — skip unchanged ones unless --force
	skipArtifact := make(map[generate.ArtifactID]bool)
	if !force && !dryRun {
		fmt.Println("Checking cache...")
		allUpToDate := true
		for _, id := range generate.AllArtifacts {
			prompt := pipeline.SystemPromptFor(id)
			sections := pipeline.RelevantSections(id)
			inputHash := cache.HashInput(specContent, sections, prompt)
			if lockFile.IsUpToDate(string(id), inputHash) {
				skipArtifact[id] = true
			} else {
				allUpToDate = false
			}
		}
		if allUpToDate {
			fmt.Println("All artifacts up to date — nothing to generate.")
			return nil
		}
	}
	pipeline.Opts.SkipArtifacts = skipArtifact

	// Run generation
	fmt.Println("Generating artifacts...")
	ctx := context.Background()
	start := time.Now()
	results, err := pipeline.Run(ctx)
	elapsed := time.Since(start)

	if err != nil {
		return err
	}

	// Display results
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "ERROR generating %s: %s\n", r.ID, r.Err)
			continue
		}
		status := "generated"
		if r.Content == "" {
			status = "skipped"
		}
		tokenInfo := ""
		if verbose && r.Response != nil {
			tokenInfo = fmt.Sprintf(" (in: %d, out: %d tokens)", r.Response.TokensIn, r.Response.TokensOut)
		}
		fmt.Printf("  %s: %s%s\n", r.ID, status, tokenInfo)
	}

	if dryRun {
		fmt.Printf("\nDry run complete (%s)\n", elapsed.Round(time.Millisecond))
		return nil
	}

	// Handle diff mode
	if diffMode {
		fmt.Println("\nDiff mode — showing changes without writing:")
		for _, r := range results {
			if r.Content == "" || r.Err != nil {
				continue
			}
			existing, err := os.ReadFile(filepath.Join(outputDir, r.FilePath))
			if err != nil {
				fmt.Printf("\n--- %s (new file) ---\n", r.FilePath)
			} else if string(existing) != r.Content {
				fmt.Printf("\n--- %s (changed) ---\n", r.FilePath)
			}
		}
		return nil
	}

	// Write artifacts to output directory
	if err := generate.WriteResults(outputDir, results); err != nil {
		return fmt.Errorf("writing artifacts: %w", err)
	}

	// Handle changelog append semantics
	for i, r := range results {
		if r.ID == generate.ArtifactChangelog && r.Content != "" {
			existingChangelog := prevArtifacts[generate.ArtifactChangelog]
			results[i].Content = generate.PrependChangelogEntry(r.Content, existingChangelog)
			changelogPath := filepath.Join(outputDir, r.FilePath)
			if err := os.MkdirAll(filepath.Dir(changelogPath), 0o755); err == nil {
				os.WriteFile(changelogPath, []byte(results[i].Content), 0o644)
			}
		}
	}

	// Update cache and lockfile
	for _, r := range results {
		if r.Err != nil || r.Content == "" {
			continue
		}
		prompt := pipeline.SystemPromptFor(r.ID)
		sections := pipeline.RelevantSections(r.ID)
		inputHash := cache.HashInput(specContent, sections, prompt)
		outputHash := cache.HashOutput(r.Content)
		model := ""
		if r.Response != nil {
			model = r.Response.Model
		}
		lockFile.UpdateEntry(string(r.ID), inputHash, outputHash, model)
		cache.WriteCached(projectDir, string(r.ID), r.Content)
	}
	cache.SaveLockFile(projectDir, lockFile)

	fmt.Printf("\nGeneration complete (%s) — output written to %s\n", elapsed.Round(time.Millisecond), outputDir)
	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	specFlag, _ := cmd.Flags().GetString("spec")
	typeFlag, _ := cmd.Flags().GetString("type")
	nameFlag, _ := cmd.Flags().GetString("name")
	force, _ := cmd.Flags().GetBool("force")

	outputFile := "COMPILER_INSTRUCTIONS.md"
	if _, err := os.Stat(outputFile); err == nil && !force {
		return fmt.Errorf("%s already exists — use --force to overwrite", outputFile)
	}

	if nameFlag == "" {
		return fmt.Errorf("--name is required")
	}

	// Build spec source for processing
	var sources []instructions.SpecSource
	switch typeFlag {
	case "cli":
		if specFlag == "" {
			return fmt.Errorf("--spec (binary name) is required for CLI type")
		}
		sources = []instructions.SpecSource{{Type: "cli", Binary: specFlag}}
	case "codebase":
		path := "."
		if specFlag != "" {
			path = specFlag
		}
		sources = []instructions.SpecSource{{Type: "codebase", Path: path}}
	default:
		if specFlag == "" {
			specFlag = "./openapi.yaml"
		}
		sources = []instructions.SpecSource{{Path: specFlag}}
	}

	// Process specs
	fmt.Println("Parsing spec sources...")
	reg := newPluginRegistry()
	parsedIR, _, err := reg.ProcessSources(sources)
	if err != nil {
		return fmt.Errorf("processing specs: %w", err)
	}

	// Resolve provider for LLM call
	resolved, err := config.Resolve("", "", "", "", nil)
	if err != nil {
		return err
	}
	prov, err := provider.New(resolved)
	if err != nil {
		return err
	}

	// Generate instructions file using LLM
	irJSON, _ := json.MarshalIndent(parsedIR, "", "  ")

	specConfig := specFlag
	if typeFlag == "cli" {
		specConfig = fmt.Sprintf("\n  type: cli\n  binary: %s", specFlag)
	} else if typeFlag == "codebase" {
		specConfig = "\n  type: codebase\n  path: ."
	}

	userMsg := fmt.Sprintf("Project name: %s\nSpec type: %s\nSpec config: %s\n\nSpec (IR):\n```json\n%s\n```",
		nameFlag, typeFlag, specConfig, string(irJSON))

	fmt.Println("Generating instructions file...")
	ctx := context.Background()
	resp, err := prov.Generate(ctx, provider.GenerateRequest{
		SystemPrompt: generate.InitPrompt,
		UserMessage:  userMsg,
		MaxTokens:    8192,
	})
	if err != nil {
		return fmt.Errorf("generating instructions: %w", err)
	}

	if err := os.WriteFile(outputFile, []byte(resp.Content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outputFile, err)
	}

	fmt.Printf("Created %s — review and customize before running `sc generate`\n", outputFile)
	return nil
}

func runValidate(cmd *cobra.Command, args []string) error {
	inst, err := instructions.Parse("COMPILER_INSTRUCTIONS.md")
	if err != nil {
		return err
	}

	hasErrors := false

	// Validate instructions
	warnings := inst.Validate()
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	// Resolve and validate spec sources
	sources, err := inst.ResolveSpecSources()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		hasErrors = true
	} else {
		reg := newPluginRegistry()
		parsedIR, parseWarnings, err := reg.ProcessSources(sources)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR parsing specs: %s\n", err)
			hasErrors = true
		} else {
			for _, w := range parseWarnings {
				fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
			}
			fmt.Printf("Spec valid: %d operations, %d types\n", len(parsedIR.Operations), len(parsedIR.Types))
		}
	}

	// Check for skills-ref validate
	if skillsRef, err := exec.LookPath("skills-ref"); err == nil {
		outputDir := inst.Frontmatter.Out
		skillDir := filepath.Join(outputDir, inst.Frontmatter.Name)
		if _, err := os.Stat(skillDir); err == nil {
			fmt.Printf("Running skills-ref validate on %s...\n", skillDir)
			validateCmd := exec.Command(skillsRef, "validate", skillDir)
			validateCmd.Stdout = os.Stdout
			validateCmd.Stderr = os.Stderr
			if err := validateCmd.Run(); err != nil {
				hasErrors = true
			}
		} else {
			fmt.Println("Skill directory not found — run `sc generate` first to validate against Agent Skills spec")
		}
	} else {
		fmt.Println("Note: Install skills-ref for Agent Skills spec validation:")
		fmt.Println("  go install github.com/agentskills/agentskills/skills-ref@latest")
	}

	if hasErrors {
		os.Exit(1)
	}
	fmt.Println("Validation passed")
	return nil
}

func runDiff(cmd *cobra.Command, args []string) error {
	againstDir, _ := cmd.Flags().GetString("against")

	projectDir, _ := os.Getwd()
	lockFile, err := cache.LoadLockFile(projectDir)
	if err != nil {
		return err
	}

	inst, err := instructions.Parse("COMPILER_INSTRUCTIONS.md")
	if err != nil {
		return err
	}

	sources, err := inst.ResolveSpecSources()
	if err != nil {
		return err
	}

	reg := newPluginRegistry()
	parsedIR, _, err := reg.ProcessSources(sources)
	if err != nil {
		return err
	}

	irJSON, _ := json.Marshal(parsedIR)
	specContent := string(irJSON)

	// Build a pipeline to get per-artifact system prompts and relevant sections
	pipeline := &generate.Pipeline{
		IR:   parsedIR,
		Inst: inst,
	}

	drifted := false
	for _, id := range generate.AllArtifacts {
		prompt := pipeline.SystemPromptFor(id)
		sections := pipeline.RelevantSections(id)
		inputHash := cache.HashInput(specContent, sections, prompt)
		if !lockFile.IsUpToDate(string(id), inputHash) {
			fmt.Printf("  DRIFTED: %s\n", id)
			drifted = true
		}
	}

	// If --against is provided, compare generated files against that directory
	if againstDir != "" {
		outputDir := inst.Frontmatter.Out
		fmt.Printf("Comparing %s against %s:\n", outputDir, againstDir)
		for _, id := range generate.AllArtifacts {
			filePath := pipeline.ArtifactPath(id)
			currentPath := filepath.Join(outputDir, filePath)
			againstPath := filepath.Join(againstDir, filePath)

			currentData, currentErr := os.ReadFile(currentPath)
			againstData, againstErr := os.ReadFile(againstPath)

			if currentErr != nil && againstErr != nil {
				continue // neither exists
			} else if currentErr != nil {
				fmt.Printf("  REMOVED: %s (exists in %s but not in %s)\n", filePath, againstDir, outputDir)
				drifted = true
			} else if againstErr != nil {
				fmt.Printf("  ADDED:   %s (exists in %s but not in %s)\n", filePath, outputDir, againstDir)
				drifted = true
			} else if string(currentData) != string(againstData) {
				fmt.Printf("  CHANGED: %s\n", filePath)
				drifted = true
			}
		}
	}

	if drifted {
		fmt.Println("\nSpec or instructions have changed since last generation.")
		fmt.Println("Run `sc generate` to update artifacts.")
		os.Exit(1)
	}

	fmt.Println("All artifacts up to date.")
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	port, _ := cmd.Flags().GetInt("port")

	if dir == "" {
		// Try to infer from instructions
		inst, err := instructions.Parse("COMPILER_INSTRUCTIONS.md")
		if err == nil {
			dir = inst.Frontmatter.Out
		} else {
			dir = "./sc-out/"
		}
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("directory %s does not exist — run `sc generate` first", dir)
	}

	addr := fmt.Sprintf("localhost:%d", port)
	fmt.Printf("Serving %s at http://%s\n", dir, addr)
	fmt.Println("Press Ctrl+C to stop")

	// Serve with conventional paths
	mux := http.NewServeMux()

	// Serve llms.txt files at root
	for _, f := range []string{"llms.txt", "llms-api.txt", "llms-full.txt"} {
		path := "/" + f
		filePath := filepath.Join(dir, f)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filePath)
		})
	}

	// Serve everything else as static files
	mux.Handle("/", http.FileServer(http.Dir(dir)))

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return server.ListenAndServe()
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	if err := config.Set(args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Set %s\n", args[0])
	return nil
}

func runConfigList(cmd *cobra.Command, args []string) error {
	values, err := config.List()
	if err != nil {
		return err
	}
	for _, key := range config.ValidKeys {
		v := values[key]
		if v == "" {
			v = "(not set)"
		}
		fmt.Printf("%-10s %s\n", key, v)
	}
	return nil
}

func runConfigReset(cmd *cobra.Command, args []string) error {
	if err := config.Reset(); err != nil {
		return err
	}
	fmt.Println("Config reset to defaults")
	return nil
}
