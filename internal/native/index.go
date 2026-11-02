// Package native indexes the built-in tool signatures of public Agents v2.11.0.
// Actual availability and validation always belong to the configured server.
package native

import (
	_ "embed"
	"encoding/json"
)

type Spec struct {
	Fields map[string]string `json:"fields"`
	Read   bool              `json:"read"`
}

//go:embed tools.json
var source []byte

var Index = func() map[string]Spec {
	var tools map[string]Spec
	if err := json.Unmarshal(source, &tools); err != nil {
		panic(err)
	}
	return tools
}()
