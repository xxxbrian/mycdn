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
	settings.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"managementSession": {Type: "apiKey", In: "cookie", Name: sessionCookieName, Description: "Log in with the environment-supplied management key to obtain a process-local session."},
	}
	settings.Security = []map[string][]string{{"managementSession": {}}}
	return humago.New(mux, settings)
}
