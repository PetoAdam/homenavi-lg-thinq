package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func main() {
	schemaBytes, err := os.ReadFile("spec/homenavi-integration.schema.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read schema: %v\n", err)
		os.Exit(1)
	}
	manifestBytes, err := os.ReadFile("manifest/homenavi-integration.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read manifest: %v\n", err)
		os.Exit(1)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", strings.NewReader(string(schemaBytes))); err != nil {
		fmt.Fprintf(os.Stderr, "load schema: %v\n", err)
		os.Exit(1)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile schema: %v\n", err)
		os.Exit(1)
	}

	var raw any
	if err := json.Unmarshal(manifestBytes, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "invalid json: %v\n", err)
		os.Exit(1)
	}
	if err := schema.Validate(raw); err != nil {
		fmt.Fprintf(os.Stderr, "schema validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("manifest validation passed")
}
