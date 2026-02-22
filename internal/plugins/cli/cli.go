package cli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/roberthamel/skill-compiler/internal/instructions"
	"github.com/roberthamel/skill-compiler/internal/ir"
)

// Plugin handles CLI binary help-tree crawling.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "cli" }

func (p *Plugin) Detect(source instructions.SpecSource) bool {
	return source.Type == "cli" && source.Binary != ""
}

func (p *Plugin) Fetch(source instructions.SpecSource) ([]byte, error) {
	binary := source.Binary
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("binary %q not found in PATH", binary)
	}

	helpFlag := source.HelpFlag
	if helpFlag == "" {
		helpFlag = "--help"
	}
	maxDepth := source.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	excludeSet := make(map[string]bool)
	for _, e := range source.Exclude {
		excludeSet[e] = true
	}

	// BFS crawl the help tree
	type cmdEntry struct {
		path  []string
		depth int
	}

	var results []crawlResult
	queue := []cmdEntry{{path: nil, depth: 0}}

	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]

		args := append(entry.path, helpFlag)
		output, err := runWithTimeout(binary, args, 5*time.Second)
		if err != nil {
			// Log warning but continue
			results = append(results, crawlResult{
				commandPath: entry.path,
				helpText:    fmt.Sprintf("(error: %s)", err),
			})
			continue
		}

		result := crawlResult{
			commandPath: entry.path,
			helpText:    output,
		}
		result.parsed = parseHelpOutput(output)
		results = append(results, result)

		// Discover subcommands for BFS
		if entry.depth < maxDepth {
			for _, sub := range result.parsed.subcommands {
				if excludeSet[sub] {
					continue
				}
				newPath := make([]string, len(entry.path))
				copy(newPath, entry.path)
				newPath = append(newPath, sub)
				queue = append(queue, cmdEntry{path: newPath, depth: entry.depth + 1})
			}
		}
	}

	// Serialize results as structured text for Parse to consume
	var buf strings.Builder
	for _, r := range results {
		cmdPath := binary
		if len(r.commandPath) > 0 {
			cmdPath = binary + " " + strings.Join(r.commandPath, " ")
		}
		fmt.Fprintf(&buf, "=== COMMAND: %s ===\n", cmdPath)
		fmt.Fprintf(&buf, "%s\n", r.helpText)
		fmt.Fprintf(&buf, "=== END ===\n\n")
	}

	return []byte(buf.String()), nil
}

type crawlResult struct {
	commandPath []string
	helpText    string
	parsed      parsedHelp
}

type parsedHelp struct {
	description string
	subcommands []string
	flags       []parsedFlag
	aliases     []string
}

type parsedFlag struct {
	name      string
	shorthand string
	flagType  string
	defVal    string
	desc      string
}

func (p *Plugin) Parse(raw []byte, source instructions.SpecSource) (*ir.IntermediateRepr, error) {
	content := string(raw)
	blocks := splitCommandBlocks(content)

	result := &ir.IntermediateRepr{
		Metadata: map[string]string{
			"binary": source.Binary,
			"type":   "cli",
		},
	}

	groupMap := make(map[string][]string)

	for _, block := range blocks {
		cmdPath := block.command
		helpText := block.text
		parsed := parseHelpOutput(helpText)

		opID := strings.ReplaceAll(cmdPath, " ", "_")
		op := ir.Operation{
			ID:          opID,
			Name:        cmdPath,
			Description: parsed.description,
			Path:        cmdPath,
			Aliases:     parsed.aliases,
			RawHelpText: helpText,
		}

		for _, f := range parsed.flags {
			op.Parameters = append(op.Parameters, ir.Parameter{
				Name:        f.name,
				In:          "flag",
				Description: f.desc,
				Type:        f.flagType,
				Default:     f.defVal,
				Shorthand:   f.shorthand,
			})
		}

		result.Operations = append(result.Operations, op)

		// Group by parent command
		parts := strings.Fields(cmdPath)
		if len(parts) > 1 {
			parent := strings.Join(parts[:len(parts)-1], " ")
			groupMap[parent] = append(groupMap[parent], opID)
		}
	}

	for name, ops := range groupMap {
		result.Groups = append(result.Groups, ir.Group{
			Name:       name,
			Operations: ops,
		})
	}

	return result, nil
}

func (p *Plugin) Validate(parsed *ir.IntermediateRepr) []ir.Warning {
	var warnings []ir.Warning
	for _, op := range parsed.Operations {
		if op.Description == "" {
			warnings = append(warnings, ir.Warning{
				Message: fmt.Sprintf("command %s has no description (help output may be non-standard)", op.Path),
			})
		}
	}
	return warnings
}

type commandBlock struct {
	command string
	text    string
}

func splitCommandBlocks(content string) []commandBlock {
	var blocks []commandBlock
	parts := strings.Split(content, "=== COMMAND: ")
	for _, part := range parts[1:] {
		endIdx := strings.Index(part, " ===\n")
		if endIdx < 0 {
			continue
		}
		cmd := part[:endIdx]
		rest := part[endIdx+5:]
		textEnd := strings.Index(rest, "\n=== END ===")
		text := rest
		if textEnd >= 0 {
			text = rest[:textEnd]
		}
		blocks = append(blocks, commandBlock{command: cmd, text: strings.TrimSpace(text)})
	}
	return blocks
}

func runWithTimeout(binary string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %s", timeout)
	}
	// Many CLIs return non-zero for --help; treat output as valid if we got output
	if len(out) > 0 {
		return string(out), nil
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var (
	// Matches lines like "  command-name    Description text"
	subcommandRe = regexp.MustCompile(`^\s{2,}(\S+)\s{2,}(.*)$`)
	// Matches flag lines like "  -f, --flag string   Description"
	flagRe = regexp.MustCompile(`^\s+(-\w),?\s+(--[\w-]+)\s+(\S+)?\s*(.*)$`)
	// Matches long-only flags like "      --flag string   Description"
	longFlagRe = regexp.MustCompile(`^\s+(--[\w-]+)\s+(\S+)?\s*(.*)$`)
	// Matches aliases line like "Aliases:\n  cmd, c"
	aliasRe = regexp.MustCompile(`(?i)aliases?:\s*\n?\s*(.+)`)
)

func parseHelpOutput(text string) parsedHelp {
	var result parsedHelp
	lines := strings.Split(text, "\n")

	// Extract description from first non-empty line(s) before sections
	var descLines []string
	inDesc := true

	section := ""
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))

		// Detect sections
		if strings.HasSuffix(lower, ":") && !strings.HasPrefix(line, " ") {
			inDesc = false
			section = strings.TrimSuffix(lower, ":")
			continue
		}
		if lower == "" {
			if inDesc && len(descLines) > 0 {
				inDesc = false
			}
			continue
		}

		if inDesc {
			descLines = append(descLines, strings.TrimSpace(line))
			continue
		}

		switch {
		case section == "available commands" || section == "commands" || section == "subcommands":
			if m := subcommandRe.FindStringSubmatch(line); m != nil {
				result.subcommands = append(result.subcommands, m[1])
			}
		case section == "flags" || section == "global flags" || section == "options":
			if m := flagRe.FindStringSubmatch(line); m != nil {
				result.flags = append(result.flags, parsedFlag{
					shorthand: m[1],
					name:      m[2],
					flagType:  m[3],
					desc:      strings.TrimSpace(m[4]),
				})
			} else if m := longFlagRe.FindStringSubmatch(line); m != nil {
				result.flags = append(result.flags, parsedFlag{
					name:     m[1],
					flagType: m[2],
					desc:     strings.TrimSpace(m[3]),
				})
			}
		}
	}

	result.description = strings.Join(descLines, " ")

	// Extract aliases
	if m := aliasRe.FindStringSubmatch(text); m != nil {
		parts := strings.Split(m[1], ",")
		for _, p := range parts {
			a := strings.TrimSpace(p)
			if a != "" {
				result.aliases = append(result.aliases, a)
			}
		}
	}

	return result
}
