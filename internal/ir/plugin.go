package ir

import (
	"fmt"

	"github.com/roberthamel/skill-compiler/internal/instructions"
)

// Warning represents a non-fatal issue found during parsing or validation.
type Warning struct {
	Message string
	Path    string // optional: file/location context
}

func (w Warning) String() string {
	if w.Path != "" {
		return fmt.Sprintf("%s: %s", w.Path, w.Message)
	}
	return w.Message
}

// SpecPlugin is the interface all spec plugins implement.
type SpecPlugin interface {
	Name() string
	Detect(source instructions.SpecSource) bool
	Fetch(source instructions.SpecSource) ([]byte, error)
	Parse(raw []byte, source instructions.SpecSource) (*IntermediateRepr, error)
	Validate(ir *IntermediateRepr) []Warning
}

// Registry holds registered spec plugins.
type Registry struct {
	plugins []SpecPlugin
}

// NewRegistry creates a new empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a plugin to the registry.
func (r *Registry) Register(p SpecPlugin) {
	r.plugins = append(r.plugins, p)
}

// Detect finds the plugin that handles the given spec source.
func (r *Registry) Detect(source instructions.SpecSource) (SpecPlugin, error) {
	for _, p := range r.plugins {
		if p.Detect(source) {
			return p, nil
		}
	}
	names := make([]string, len(r.plugins))
	for i, p := range r.plugins {
		names[i] = p.Name()
	}
	return nil, fmt.Errorf("no plugin can handle spec source (registered: %v)", names)
}

// ProcessSources resolves, fetches, parses, and merges all spec sources into a single IR.
func (r *Registry) ProcessSources(sources []instructions.SpecSource) (*IntermediateRepr, []Warning, error) {
	merged := &IntermediateRepr{
		Metadata: make(map[string]string),
	}
	var allWarnings []Warning

	for _, src := range sources {
		plugin, err := r.Detect(src)
		if err != nil {
			return nil, nil, err
		}

		raw, err := plugin.Fetch(src)
		if err != nil {
			return nil, nil, fmt.Errorf("[%s] fetch: %w", plugin.Name(), err)
		}

		parsed, err := plugin.Parse(raw, src)
		if err != nil {
			return nil, nil, fmt.Errorf("[%s] parse: %w", plugin.Name(), err)
		}

		warnings := plugin.Validate(parsed)
		allWarnings = append(allWarnings, warnings...)

		merged.Merge(parsed)
	}

	return merged, allWarnings, nil
}
