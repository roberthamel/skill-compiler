package codebase

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/roberthamel/skill-compiler/internal/instructions"
	"github.com/roberthamel/skill-compiler/internal/ir"
)

// Plugin handles codebase directory scanning.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "codebase" }

func (p *Plugin) Detect(source instructions.SpecSource) bool {
	return source.Type == "codebase"
}

func (p *Plugin) Fetch(source instructions.SpecSource) ([]byte, error) {
	root := source.Path
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %w", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("accessing path %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path %s is not a directory", root)
	}

	maxFiles := source.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 1000
	}

	// Load gitignore patterns
	gitignorePatterns := loadGitignore(root)

	// Scan file tree
	var entries []fileInfo
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		// Skip hidden dirs (except . files at root like .eslintrc)
		if info.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			if base == "node_modules" || base == "vendor" || base == "__pycache__" || base == "target" || base == "dist" || base == "build" {
				return filepath.SkipDir
			}
		}

		// Apply gitignore
		if matchesAny(rel, gitignorePatterns) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply include/exclude
		if len(source.Include) > 0 && !info.IsDir() {
			matched := false
			for _, pattern := range source.Include {
				if m, _ := filepath.Match(pattern, filepath.Base(path)); m {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
		for _, pattern := range source.Exclude {
			if m, _ := filepath.Match(pattern, filepath.Base(path)); m {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		entries = append(entries, fileInfo{
			rel:   rel,
			isDir: info.IsDir(),
			size:  info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning directory: %w", err)
	}

	// Prioritize files if exceeding limit
	if len(entries) > maxFiles {
		log.Printf("WARNING: codebase scan found %d files, truncating to %d (prioritizing key files)", len(entries), maxFiles)
		entries = prioritizeFiles(entries, maxFiles)
	}

	// Serialize as JSON for Parse to consume
	data, err := json.Marshal(scanResult{Root: root, Entries: entries})
	if err != nil {
		return nil, err
	}
	return data, nil
}

type fileInfo struct {
	rel   string
	isDir bool
	size  int64
}

func (f fileInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path  string `json:"path"`
		IsDir bool   `json:"isDir,omitempty"`
		Size  int64  `json:"size,omitempty"`
	}{f.rel, f.isDir, f.size})
}

func (f *fileInfo) UnmarshalJSON(data []byte) error {
	var v struct {
		Path  string `json:"path"`
		IsDir bool   `json:"isDir,omitempty"`
		Size  int64  `json:"size,omitempty"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	f.rel = v.Path
	f.isDir = v.IsDir
	f.size = v.Size
	return nil
}

type scanResult struct {
	Root    string     `json:"root"`
	Entries []fileInfo `json:"entries"`
}

func (p *Plugin) Parse(raw []byte, source instructions.SpecSource) (*ir.IntermediateRepr, error) {
	var scan scanResult
	if err := json.Unmarshal(raw, &scan); err != nil {
		return nil, fmt.Errorf("parsing scan result: %w", err)
	}

	structure := &ir.ProjectStructure{}

	// Build file tree
	for _, e := range scan.Entries {
		structure.FileTree = append(structure.FileTree, ir.FileEntry{
			Path:  e.rel,
			IsDir: e.isDir,
			Size:  e.size,
		})
	}

	// Detect and parse manifests
	stack := &ir.StackInfo{
		Dependencies: make(map[string]string),
		Scripts:      make(map[string]string),
	}

	for _, e := range scan.Entries {
		if e.isDir {
			continue
		}
		fullPath := filepath.Join(scan.Root, e.rel)
		base := filepath.Base(e.rel)

		switch base {
		case "package.json":
			parsePackageJSON(fullPath, stack)
		case "go.mod":
			parseGoMod(fullPath, stack)
		case "Cargo.toml":
			stack.Languages = appendUniq(stack.Languages, "Rust")
			stack.BuildTools = appendUniq(stack.BuildTools, "Cargo")
		case "pyproject.toml":
			stack.Languages = appendUniq(stack.Languages, "Python")
		case "tsconfig.json":
			stack.Languages = appendUniq(stack.Languages, "TypeScript")
			readConfigFile(fullPath, e.rel, structure)
		case ".eslintrc", ".eslintrc.json", ".eslintrc.js":
			readConfigFile(fullPath, e.rel, structure)
		case "Dockerfile":
			stack.BuildTools = appendUniq(stack.BuildTools, "Docker")
			readConfigFile(fullPath, e.rel, structure)
		}

		// CI configs
		if strings.Contains(e.rel, ".github/workflows/") || strings.Contains(e.rel, ".gitlab-ci") {
			readConfigFile(fullPath, e.rel, structure)
		}

		// Agent instruction files
		switch base {
		case "CLAUDE.md", "AGENTS.md", "CONTRIBUTING.md", "README.md":
			readDocFile(fullPath, e.rel, structure)
		}

		// Key source files
		if isKeyFile(e.rel) {
			content := readFileContent(fullPath, 50000)
			if content != "" {
				role := classifyFile(e.rel)
				structure.KeyFiles = append(structure.KeyFiles, ir.KeyFile{
					Path:    e.rel,
					Content: content,
					Role:    role,
				})
			}
		}
	}

	structure.Stack = stack

	return &ir.IntermediateRepr{
		Structure: structure,
		Metadata: map[string]string{
			"type": "codebase",
			"root": scan.Root,
		},
	}, nil
}

func (p *Plugin) Validate(parsed *ir.IntermediateRepr) []ir.Warning {
	var warnings []ir.Warning
	if parsed.Structure == nil {
		warnings = append(warnings, ir.Warning{Message: "codebase scan produced no structure"})
	} else if parsed.Structure.Stack == nil {
		warnings = append(warnings, ir.Warning{Message: "could not detect technology stack"})
	}
	return warnings
}

func parsePackageJSON(path string, stack *ir.StackInfo) {
	data := readFileContent(path, 100000)
	if data == "" {
		return
	}
	var pkg struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
		Scripts      map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(data), &pkg); err != nil {
		return
	}
	stack.Languages = appendUniq(stack.Languages, "JavaScript")
	for name, ver := range pkg.Dependencies {
		stack.Dependencies[name] = ver
		// Detect frameworks
		switch {
		case name == "react":
			stack.Frameworks = appendUniq(stack.Frameworks, "React")
		case name == "vue":
			stack.Frameworks = appendUniq(stack.Frameworks, "Vue")
		case name == "express":
			stack.Frameworks = appendUniq(stack.Frameworks, "Express")
		case name == "next":
			stack.Frameworks = appendUniq(stack.Frameworks, "Next.js")
		}
	}
	for name, script := range pkg.Scripts {
		stack.Scripts[name] = script
	}
}

func parseGoMod(path string, stack *ir.StackInfo) {
	data := readFileContent(path, 100000)
	if data == "" {
		return
	}
	stack.Languages = appendUniq(stack.Languages, "Go")
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "require ") || (len(line) > 0 && !strings.HasPrefix(line, "//") && !strings.HasPrefix(line, "module") && !strings.HasPrefix(line, "go ") && !strings.HasPrefix(line, ")") && !strings.HasPrefix(line, "(")) {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				stack.Dependencies[parts[0]] = parts[1]
			}
		}
	}
}

func readConfigFile(fullPath, relPath string, structure *ir.ProjectStructure) {
	content := readFileContent(fullPath, 50000)
	if content != "" {
		structure.ConfigFiles = append(structure.ConfigFiles, ir.ConfigFile{
			Path:    relPath,
			Content: content,
		})
	}
}

func readDocFile(fullPath, relPath string, structure *ir.ProjectStructure) {
	content := readFileContent(fullPath, 100000)
	if content != "" {
		structure.Docs = append(structure.Docs, ir.DocFile{
			Path:    relPath,
			Content: content,
		})
	}
}

func readFileContent(path string, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
	}
	return string(data)
}

func isKeyFile(rel string) bool {
	base := filepath.Base(rel)
	lower := strings.ToLower(base)

	// Entry points
	if lower == "main.go" || lower == "main.ts" || lower == "main.js" || lower == "index.ts" || lower == "index.js" || lower == "app.ts" || lower == "app.js" {
		return true
	}
	// Route definitions
	if strings.Contains(lower, "route") || strings.Contains(lower, "router") {
		return true
	}
	// Schema files
	if strings.Contains(lower, "schema") || strings.Contains(lower, "model") {
		return true
	}
	// Test setup
	if lower == "jest.config.js" || lower == "jest.config.ts" || lower == "vitest.config.ts" || lower == "setup.ts" || lower == "setup.js" {
		return true
	}
	return false
}

func classifyFile(rel string) string {
	lower := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.HasPrefix(lower, "main.") || strings.HasPrefix(lower, "index.") || strings.HasPrefix(lower, "app."):
		return "entrypoint"
	case strings.Contains(lower, "route") || strings.Contains(lower, "router"):
		return "routes"
	case strings.Contains(lower, "schema") || strings.Contains(lower, "model"):
		return "schema"
	case strings.Contains(lower, "test") || strings.Contains(lower, "spec") || strings.Contains(lower, "setup"):
		return "test-setup"
	default:
		return ""
	}
}

func loadGitignore(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func matchesAny(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSuffix(pattern, "/")
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(rel)); matched {
			return true
		}
		// Check if any path component matches
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}
	return false
}

func prioritizeFiles(entries []fileInfo, maxFiles int) []fileInfo {
	// Score files by importance
	type scored struct {
		entry fileInfo
		score int
	}
	var scored2 []scored
	for _, e := range entries {
		s := 0
		if e.isDir {
			s = 1
		}
		base := strings.ToLower(filepath.Base(e.rel))
		switch {
		case base == "package.json" || base == "go.mod" || base == "cargo.toml" || base == "pyproject.toml":
			s = 100
		case base == "readme.md" || base == "claude.md" || base == "agents.md" || base == "contributing.md":
			s = 90
		case strings.Contains(base, "config") || base == "dockerfile" || base == "tsconfig.json":
			s = 80
		case isKeyFile(e.rel):
			s = 70
		default:
			s = 10
		}
		scored2 = append(scored2, scored{entry: e, score: s})
	}
	sort.Slice(scored2, func(i, j int) bool {
		return scored2[i].score > scored2[j].score
	})
	result := make([]fileInfo, 0, maxFiles)
	for i := 0; i < maxFiles && i < len(scored2); i++ {
		result = append(result, scored2[i].entry)
	}
	return result
}

func appendUniq(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}
