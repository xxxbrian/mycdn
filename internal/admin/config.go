package admin

import (
	"context"
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/config"
)

type configOutput struct {
	ETag string `header:"ETag"`
	Body config.Document
}

type saveConfigInput struct {
	IfMatch string `header:"If-Match" required:"true" doc:"Exact ETag returned by GET /api/config."`
	Body    config.Document
}

func (s *Server) getConfig(_ context.Context, _ *struct{}) (*configOutput, error) {
	snapshot := s.config.Get()
	return &configOutput{ETag: quotedRevision(snapshot), Body: snapshot.Document()}, nil
}

func (s *Server) saveConfig(_ context.Context, input *saveConfigInput) (*configOutput, error) {
	// Store.Save serializes persistence, but the If-Match check must share the
	// same critical section. Every management save goes through this lock.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if !matchesRevision(input.IfMatch, quotedRevision(s.config.Get())) {
		return nil, huma.Error412PreconditionFailed("configuration changed; reload it before saving")
	}
	if err := s.config.Save(input.Body); err != nil {
		return nil, managementError("save configuration", err)
	}
	snapshot := s.config.Get()
	return &configOutput{ETag: quotedRevision(snapshot), Body: snapshot.Document()}, nil
}

func quotedRevision(snapshot *config.Snapshot) string { return `"` + snapshot.Revision() + `"` }

func matchesRevision(ifMatch, current string) bool {
	for _, candidate := range strings.Split(ifMatch, ",") {
		if strings.TrimSpace(candidate) == current {
			return true
		}
	}
	// A wildcard would defeat the editor's lost-update protection. Require an
	// actual strong revision, not W/"..." or '*'.
	return false
}

func (s *Server) restartFields() []string {
	current := s.config.Get().Document()
	fields := []string{}
	if current.Listen.Proxy != s.startup.Listen.Proxy {
		fields = append(fields, "listen.proxy")
	}
	if current.Listen.Admin != s.startup.Listen.Admin {
		fields = append(fields, "listen.admin")
	}
	if current.DataDir != s.startup.DataDir {
		fields = append(fields, "data_dir")
	}
	if !reflect.DeepEqual(current.Storage, s.startup.Storage) {
		fields = append(fields, "storage")
	}
	if !reflect.DeepEqual(current.Analytics, s.startup.Analytics) {
		fields = append(fields, "analytics")
	}
	return fields
}

func (s *Server) configRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-config", Method: http.MethodGet, Path: "/api/config",
		Summary: "Read the saved configuration and its strong revision",
	}, s.getConfig)
	huma.Register(api, huma.Operation{
		OperationID: "save-config", Method: http.MethodPut, Path: "/api/config",
		Summary:      "Validate and atomically save the whole configuration",
		Description:  "Listeners, data directory, storage budgets, and analytics settings require a process restart. Other settings are published atomically for new requests.",
		MaxBodyBytes: 4 << 20, Errors: []int{400, 403, 412, 413, 422},
	}, s.saveConfig)
}
