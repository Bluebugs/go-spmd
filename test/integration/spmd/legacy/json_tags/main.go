// Legacy compatibility test: using 'uniform' and 'varying' in struct tags
// This ensures existing code continues to work with SPMD extensions
package main

import (
	"encoding/json"
	"fmt"
	"log"
)

func main() {
	// Test struct with field names and JSON tags using 'uniform' and 'varying'
	config := Config{
		Uniform: 42,
		Varying: "test value", 
		Data: DataStruct{
			UniformField: 100,
			VaryingField: "nested",
		},
	}
	
	// Test JSON marshaling
	jsonData, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err)
	}
	
	fmt.Printf("JSON output: %s\n", jsonData)
	
	// Test JSON unmarshaling
	jsonInput := `{
		"uniform_value": 999,
		"varying_value": "from json",
		"nested_data": {
			"uniform_field": 777,
			"varying_field": "nested json"
		}
	}`
	
	var decoded Config
	err = json.Unmarshal([]byte(jsonInput), &decoded)
	if err != nil {
		log.Fatal(err)
	}
	
	fmt.Printf("Decoded uniform: %d\n", decoded.Uniform)
	fmt.Printf("Decoded varying: %s\n", decoded.Varying)
	fmt.Printf("Decoded nested uniform: %d\n", decoded.Data.UniformField)
	fmt.Printf("Decoded nested varying: %s\n", decoded.Data.VaryingField)
	
	// Test struct field access
	fmt.Printf("Direct access uniform: %d\n", config.Uniform)
	fmt.Printf("Direct access varying: %s\n", config.Varying)
}

// Struct with fields and tags using 'uniform' and 'varying'  
type Config struct {
	Uniform int        `json:"uniform_value" yaml:"uniform" xml:"uniform"`
	Varying string     `json:"varying_value" yaml:"varying" xml:"varying"`
	Data    DataStruct `json:"nested_data" yaml:"data" xml:"data"`
}

type DataStruct struct {
	UniformField int    `json:"uniform_field" db:"uniform_col"`
	VaryingField string `json:"varying_field" db:"varying_col"`
}

// Test method names on structs with these field names
func (c Config) GetUniform() int {
	return c.Uniform
}

func (c Config) GetVarying() string {
	return c.Varying
}

func (c *Config) SetUniform(val int) {
	c.Uniform = val
}

func (c *Config) SetVarying(val string) {
	c.Varying = val
}