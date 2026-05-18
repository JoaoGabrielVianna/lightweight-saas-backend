package bootstrap

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaJSON is the canonical JSON Schema for project.json. Embedded at
// build time so validation has zero filesystem dependency at runtime; the
// `config/project.schema.json` file is the SAME bytes, kept for IDE
// autocomplete (via the $schema reference inside project.json).
//
//go:embed schema/project.schema.json
var schemaJSON []byte

// compiledSchema is built lazily on first use, then cached. Compilation is
// not cheap; doing it once per process keeps `bootstrap.Load` snappy in tests.
var compiledSchema *jsonschema.Schema

func loadSchema() (*jsonschema.Schema, error) {
	if compiledSchema != nil {
		return compiledSchema, nil
	}
	var raw any
	if err := json.Unmarshal(schemaJSON, &raw); err != nil {
		return nil, fmt.Errorf("parse embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("project.schema.json", raw); err != nil {
		return nil, fmt.Errorf("register schema: %w", err)
	}
	sch, err := c.Compile("project.schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	compiledSchema = sch
	return sch, nil
}

// validateAgainstSchema parses the JSON bytes and runs them through the
// embedded JSON Schema. Returns a wrapped error on the first failure.
func validateAgainstSchema(raw []byte) error {
	sch, err := loadSchema()
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		return err
	}
	return nil
}
