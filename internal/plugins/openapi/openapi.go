package openapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/roberthamel/skill-compiler/internal/instructions"
	"github.com/roberthamel/skill-compiler/internal/ir"
	"gopkg.in/yaml.v3"
)

// Plugin handles OpenAPI 3.x spec sources.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "openapi" }

func (p *Plugin) Detect(source instructions.SpecSource) bool {
	if source.Type == "openapi" {
		return true
	}
	if source.Path != "" {
		ext := strings.ToLower(filepath.Ext(source.Path))
		return ext == ".yaml" || ext == ".yml" || ext == ".json"
	}
	if source.URL != "" || source.Command != "" {
		// URL and command sources need explicit type
		return source.Type == "openapi"
	}
	return false
}

func (p *Plugin) Fetch(source instructions.SpecSource) ([]byte, error) {
	if source.Path != "" {
		return os.ReadFile(source.Path)
	}
	if source.URL != "" {
		resp, err := http.Get(source.URL)
		if err != nil {
			return nil, fmt.Errorf("fetching URL %s: %w", source.URL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching URL %s: HTTP %d", source.URL, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	if source.Command != "" {
		parts := strings.Fields(source.Command)
		cmd := exec.Command(parts[0], parts[1:]...)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("running command %q: %w", source.Command, err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("openapi plugin: no path, url, or command in spec source")
}

// openAPIDoc is a minimal representation for parsing.
type openAPIDoc struct {
	OpenAPI    string                          `yaml:"openapi" json:"openapi"`
	Info       openAPIInfo                     `yaml:"info" json:"info"`
	Paths      map[string]map[string]openAPIOp `yaml:"paths" json:"paths"`
	Components *openAPIComponents              `yaml:"components" json:"components"`
}

type openAPIInfo struct {
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Version     string `yaml:"version" json:"version"`
}

type openAPIOp struct {
	OperationID string                 `yaml:"operationId" json:"operationId"`
	Summary     string                 `yaml:"summary" json:"summary"`
	Description string                 `yaml:"description" json:"description"`
	Tags        []string               `yaml:"tags" json:"tags"`
	Deprecated  bool                   `yaml:"deprecated" json:"deprecated"`
	Security    []map[string][]string  `yaml:"security" json:"security"`
	Parameters  []openAPIParam         `yaml:"parameters" json:"parameters"`
	RequestBody *openAPIReqBody        `yaml:"requestBody" json:"requestBody"`
	Responses   map[string]openAPIResp `yaml:"responses" json:"responses"`
}

type openAPIParam struct {
	Name        string         `yaml:"name" json:"name"`
	In          string         `yaml:"in" json:"in"`
	Description string         `yaml:"description" json:"description"`
	Required    bool           `yaml:"required" json:"required"`
	Schema      *openAPISchema `yaml:"schema" json:"schema"`
	Ref         string         `yaml:"$ref" json:"$ref"`
}

type openAPIReqBody struct {
	Description string                      `yaml:"description" json:"description"`
	Required    bool                        `yaml:"required" json:"required"`
	Content     map[string]openAPIMediaType `yaml:"content" json:"content"`
}

type openAPIMediaType struct {
	Schema *openAPISchema `yaml:"schema" json:"schema"`
}

type openAPIResp struct {
	Description string                      `yaml:"description" json:"description"`
	Content     map[string]openAPIMediaType `yaml:"content" json:"content"`
}

type openAPISchema struct {
	Ref         string                    `yaml:"$ref" json:"$ref"`
	Type        string                    `yaml:"type" json:"type"`
	Format      string                    `yaml:"format" json:"format"`
	Description string                    `yaml:"description" json:"description"`
	Properties  map[string]*openAPISchema `yaml:"properties" json:"properties"`
	Items       *openAPISchema            `yaml:"items" json:"items"`
	Required    []string                  `yaml:"required" json:"required"`
	Enum        []string                  `yaml:"enum" json:"enum"`
}

type openAPIComponents struct {
	Schemas         map[string]*openAPISchema         `yaml:"schemas" json:"schemas"`
	SecuritySchemes map[string]*openAPISecurityScheme `yaml:"securitySchemes" json:"securitySchemes"`
	Parameters      map[string]*openAPIParam          `yaml:"parameters" json:"parameters"`
}

type openAPISecurityScheme struct {
	Type        string `yaml:"type" json:"type"`
	Name        string `yaml:"name" json:"name"`
	In          string `yaml:"in" json:"in"`
	Scheme      string `yaml:"scheme" json:"scheme"`
	Description string `yaml:"description" json:"description"`
}

func (p *Plugin) Parse(raw []byte, source instructions.SpecSource) (*ir.IntermediateRepr, error) {
	// Try to resolve $ref references by building a raw document map first
	var rawDoc map[string]interface{}
	if err := yaml.Unmarshal(raw, &rawDoc); err != nil {
		// Try JSON
		if err2 := json.Unmarshal(raw, &rawDoc); err2 != nil {
			return nil, fmt.Errorf("parsing OpenAPI document: %w", err)
		}
	}
	resolveRefs(rawDoc, rawDoc)

	// Re-marshal and unmarshal into typed struct
	resolved, err := yaml.Marshal(rawDoc)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling resolved document: %w", err)
	}

	var doc openAPIDoc
	if err := yaml.Unmarshal(resolved, &doc); err != nil {
		return nil, fmt.Errorf("parsing resolved OpenAPI document: %w", err)
	}

	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		return nil, fmt.Errorf("unsupported OpenAPI version: %q (only 3.x supported)", doc.OpenAPI)
	}

	result := &ir.IntermediateRepr{
		Metadata: map[string]string{
			"title":       doc.Info.Title,
			"description": doc.Info.Description,
			"version":     doc.Info.Version,
		},
	}

	// Parse operations from paths (sorted for deterministic output)
	groupOps := make(map[string][]string)
	sortedPaths := make([]string, 0, len(doc.Paths))
	for path := range doc.Paths {
		sortedPaths = append(sortedPaths, path)
	}
	sort.Strings(sortedPaths)
	for _, path := range sortedPaths {
		methods := doc.Paths[path]
		sortedMethods := make([]string, 0, len(methods))
		for method := range methods {
			sortedMethods = append(sortedMethods, method)
		}
		sort.Strings(sortedMethods)
		for _, method := range sortedMethods {
			op := methods[method]
			opID := op.OperationID
			if opID == "" {
				opID = strings.ToLower(method) + "_" + strings.ReplaceAll(strings.Trim(path, "/"), "/", "_")
			}

			desc := op.Description
			if desc == "" {
				desc = op.Summary
			}

			irOp := ir.Operation{
				ID:          opID,
				Name:        op.Summary,
				Description: desc,
				Method:      strings.ToUpper(method),
				Path:        path,
				Tags:        op.Tags,
				Deprecated:  op.Deprecated,
			}

			// Parameters
			for _, param := range op.Parameters {
				irOp.Parameters = append(irOp.Parameters, ir.Parameter{
					Name:        param.Name,
					In:          param.In,
					Description: param.Description,
					Required:    param.Required,
					Type:        schemaType(param.Schema),
				})
			}

			// Request body
			if op.RequestBody != nil {
				for ct, mt := range op.RequestBody.Content {
					typeName := ""
					if mt.Schema != nil && mt.Schema.Ref != "" {
						typeName = refName(mt.Schema.Ref)
					}
					irOp.RequestBody = &ir.TypeRef{
						TypeName:    typeName,
						Description: op.RequestBody.Description,
						ContentType: ct,
					}
					break // take first content type
				}
			}

			// Responses
			codes := make([]string, 0, len(op.Responses))
			for code := range op.Responses {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			for _, code := range codes {
				resp := op.Responses[code]
				irResp := ir.Response{
					StatusCode:  code,
					Description: resp.Description,
				}
				for ct, mt := range resp.Content {
					typeName := ""
					if mt.Schema != nil && mt.Schema.Ref != "" {
						typeName = refName(mt.Schema.Ref)
					}
					irResp.Body = &ir.TypeRef{
						TypeName:    typeName,
						ContentType: ct,
					}
					break
				}
				irOp.Responses = append(irOp.Responses, irResp)
			}

			// Auth references (sorted for deterministic output)
			for _, sec := range op.Security {
				secNames := make([]string, 0, len(sec))
				for name := range sec {
					secNames = append(secNames, name)
				}
				sort.Strings(secNames)
				for _, name := range secNames {
					irOp.Auth = append(irOp.Auth, name)
				}
			}

			result.Operations = append(result.Operations, irOp)

			// Group by tags
			for _, tag := range op.Tags {
				groupOps[tag] = append(groupOps[tag], opID)
			}
		}
	}

	// Parse types from components/schemas (sorted for deterministic output)
	if doc.Components != nil {
		sortedSchemas := make([]string, 0, len(doc.Components.Schemas))
		for name := range doc.Components.Schemas {
			sortedSchemas = append(sortedSchemas, name)
		}
		sort.Strings(sortedSchemas)
		for _, name := range sortedSchemas {
			schema := doc.Components.Schemas[name]
			td := ir.TypeDef{
				Name:        name,
				Description: schema.Description,
				Enum:        schema.Enum,
			}
			sortedFields := make([]string, 0, len(schema.Properties))
			for fieldName := range schema.Properties {
				sortedFields = append(sortedFields, fieldName)
			}
			sort.Strings(sortedFields)
			for _, fieldName := range sortedFields {
				fieldSchema := schema.Properties[fieldName]
				required := false
				for _, req := range schema.Required {
					if req == fieldName {
						required = true
						break
					}
				}
				td.Fields = append(td.Fields, ir.TypeField{
					Name:        fieldName,
					Type:        schemaType(fieldSchema),
					Description: fieldSchema.Description,
					Required:    required,
				})
			}
			result.Types = append(result.Types, td)
		}

		// Parse auth schemes (sorted for deterministic output)
		sortedSecSchemes := make([]string, 0, len(doc.Components.SecuritySchemes))
		for name := range doc.Components.SecuritySchemes {
			sortedSecSchemes = append(sortedSecSchemes, name)
		}
		sort.Strings(sortedSecSchemes)
		for _, name := range sortedSecSchemes {
			scheme := doc.Components.SecuritySchemes[name]
			result.Auth = append(result.Auth, ir.AuthScheme{
				ID:          name,
				Type:        scheme.Type,
				Name:        scheme.Name,
				In:          scheme.In,
				Scheme:      scheme.Scheme,
				Description: scheme.Description,
			})
		}
	}

	// Build groups (sorted for deterministic output)
	sortedGroups := make([]string, 0, len(groupOps))
	for name := range groupOps {
		sortedGroups = append(sortedGroups, name)
	}
	sort.Strings(sortedGroups)
	for _, name := range sortedGroups {
		ops := groupOps[name]
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
		if op.Description == "" && op.Name == "" {
			warnings = append(warnings, ir.Warning{
				Message: fmt.Sprintf("operation %s has no description or summary", op.ID),
			})
		}
		for _, param := range op.Parameters {
			if param.Description == "" {
				warnings = append(warnings, ir.Warning{
					Message: fmt.Sprintf("parameter %s in %s %s has no description", param.Name, op.Method, op.Path),
				})
			}
		}
	}
	return warnings
}

// resolveRefs recursively resolves $ref pointers within the document.
func resolveRefs(node interface{}, root map[string]interface{}) {
	switch v := node.(type) {
	case map[string]interface{}:
		if ref, ok := v["$ref"].(string); ok {
			resolved := lookupRef(ref, root)
			if resolved != nil {
				// Copy resolved fields into this map (in-place resolution)
				if rm, ok := resolved.(map[string]interface{}); ok {
					delete(v, "$ref")
					for k, val := range rm {
						v[k] = val
					}
				}
			}
		}
		for _, val := range v {
			resolveRefs(val, root)
		}
	case []interface{}:
		for _, item := range v {
			resolveRefs(item, root)
		}
	}
}

// lookupRef resolves a JSON pointer like #/components/schemas/Foo.
func lookupRef(ref string, root map[string]interface{}) interface{} {
	if !strings.HasPrefix(ref, "#/") {
		return nil // external refs not supported in v1
	}
	parts := strings.Split(ref[2:], "/")
	var current interface{} = root
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[part]
	}
	return current
}

func schemaType(s *openAPISchema) string {
	if s == nil {
		return ""
	}
	if s.Type == "array" && s.Items != nil {
		return "[]" + schemaType(s.Items)
	}
	if s.Format != "" {
		return s.Type + "(" + s.Format + ")"
	}
	return s.Type
}

func refName(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}
