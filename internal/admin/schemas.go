package admin

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func prepareSchemas(api huma.API) {
	registry := api.OpenAPI().Components.Schemas
	registry.Schema(reflect.TypeFor[config.Document](), true, "")
	branch := registry.Schema(reflect.TypeFor[rules.Branch](), false, "")
	condition := branch.Properties["condition"]
	// Huma does not infer nullable object pointers. In the rule AST a required,
	// explicitly null condition means ELSE, while nested conditions retain their
	// real recursive schema references. Keep that distinction in validation and
	// the generated OpenAPI without creating a second config representation.
	branch.Properties["condition"] = &huma.Schema{AnyOf: []*huma.Schema{
		condition,
		{Type: "null", Enum: []any{nil}},
	}}
	branch.PrecomputeMessages()
}
