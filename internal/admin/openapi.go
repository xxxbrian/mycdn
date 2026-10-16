package admin

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

func newAPI(mux *http.ServeMux) huma.API {
	settings := huma.DefaultConfig("mycdn", "0.1.0")
	settings.OpenAPIPath = "/api/openapi"
	settings.DocsPath = ""
	settings.SchemasPath = ""
	// Keep configuration documents as plain JSON. The generated specification
	// remains available, but schema-link fields are not part of saved config.
	settings.CreateHooks = nil
	settings.Transformers = nil
	return humago.New(mux, settings)
}
