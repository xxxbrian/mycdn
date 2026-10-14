package rules

import (
	"reflect"
	"strings"
)

// Catalog is the source for both validation and editor choices. Parameter names
// refer to the typed Action member, not arbitrary runtime JSON.
type Catalog struct {
	Stages  []Stage                    `json:"stages"`
	Fields  []FieldCapability          `json:"fields"`
	Actions []ActionCapability         `json:"actions"`
	Schemas map[string]ParameterSchema `json:"schemas"`
}

// ParameterSchema is a compact JSON-Schema-compatible subset for typed action
// parameters. Pointer fields are optional patches; false and zero remain values.
type ParameterSchema struct {
	Type       string                     `json:"type"`
	Properties map[string]ParameterSchema `json:"properties,omitempty"`
	Items      *ParameterSchema           `json:"items,omitempty"`
	Required   []string                   `json:"required,omitempty"`
	Enum       []string                   `json:"enum,omitempty"`
}

type FieldCapability struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Key       bool     `json:"key"`
	Stages    []Stage  `json:"stages"`
	Operators []string `json:"operators"`
}

type ActionCapability struct {
	Type      string  `json:"type"`
	Parameter string  `json:"parameter"`
	Stages    []Stage `json:"stages"`
}

func Capabilities() Catalog {
	all := []Stage{Ingress, Request, OriginResponse, Delivery}
	stringOps := []string{"eq", "ne", "in", "not_in", "prefix", "suffix", "contains", "glob", "regex", "exists", "not_exists"}
	var fields []FieldCapability
	for _, name := range []string{"site", "host", "path", "url", "query", "extension", "filename", "method", "header", "cookie", "client_ip", "peer_ip", "http_version", "scheme"} {
		ops, typ := stringOps, "string"
		if name == "client_ip" || name == "peer_ip" {
			ops, typ = []string{"eq", "ne", "in", "not_in", "cidr", "exists", "not_exists"}, "ip"
		}
		key := name == "query" || name == "header" || name == "cookie"
		stages := all
		if name == "client_ip" {
			stages = []Stage{Request, OriginResponse, Delivery}
		}
		fields = append(fields, FieldCapability{name, typ, key, stages, ops})
		fields = append(fields, FieldCapability{"original." + name, typ, key, stages, ops})
	}
	fields = append(fields,
		FieldCapability{"status", "integer", false, []Stage{OriginResponse, Delivery}, []string{"eq", "ne", "in", "not_in", "gt", "gte", "lt", "lte"}},
		FieldCapability{"response_header", "string", true, []Stage{OriginResponse, Delivery}, stringOps},
		FieldCapability{"cache_result", "string", false, []Stage{Delivery}, []string{"eq", "ne", "in", "not_in", "exists", "not_exists"}},
	)
	actions := []ActionCapability{
		{"identity", "identity", []Stage{Ingress}},
		{"limits", "limits", []Stage{Ingress, Request}},
		{"cache", "cache", []Stage{Request, OriginResponse}},
		{"cache_key", "cache_key", []Stage{Request}},
		{"origin", "origin", []Stage{Request}},
		{"path", "path", []Stage{Request}},
		{"query", "query", []Stage{Request}},
		{"cookie", "cookie", []Stage{Request}},
		{"request_header", "header", []Stage{Request}},
		{"authority", "authority", []Stage{Request}},
		{"upstream", "upstream", []Stage{Request}},
		{"source_header", "header", []Stage{OriginResponse}},
		{"response_header", "header", []Stage{Ingress, Request, Delivery}},
		{"browser_ttl", "browser_ttl_seconds", []Stage{Ingress, Request, Delivery}},
		{"cors", "cors", []Stage{Ingress, Request, Delivery}},
		{"reply", "reply", []Stage{Ingress, Request, Delivery}},
		{"redirect", "redirect", []Stage{Ingress, Request, Delivery}},
	}
	schemas := make(map[string]ParameterSchema)
	actionType := reflect.TypeFor[Action]()
	for _, a := range actions {
		for i := 0; i < actionType.NumField(); i++ {
			field := actionType.Field(i)
			if strings.Split(field.Tag.Get("json"), ",")[0] == a.Parameter {
				schemas[a.Parameter] = parameterSchema(field.Type)
				break
			}
		}
	}
	return Catalog{Stages: all, Fields: fields, Actions: actions, Schemas: schemas}
}

func parameterSchema(t reflect.Type) ParameterSchema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s := ParameterSchema{}
	switch t.Kind() {
	case reflect.Struct:
		s.Type = "object"
		s.Properties = make(map[string]ParameterSchema)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			s.Properties[tag[0]] = parameterSchema(field.Type)
			if field.Type.Kind() != reflect.Pointer && !strings.Contains(field.Tag.Get("json"), "omitempty") {
				s.Required = append(s.Required, tag[0])
			}
		}
	case reflect.Slice:
		s.Type = "array"
		item := parameterSchema(t.Elem())
		s.Items = &item
	case reflect.Bool:
		s.Type = "boolean"
	case reflect.Int, reflect.Int64:
		s.Type = "integer"
	default:
		s.Type = "string"
	}
	if t == reflect.TypeFor[CacheKey]() {
		setEnum(s.Properties, "query_mode", "all", "ignore", "include", "exclude")
	}
	if t == reflect.TypeFor[CachePatch]() {
		setEnum(s.Properties, "mode", "respect", "force", "bypass")
	}
	if t == reflect.TypeFor[UpstreamPatch]() {
		setEnum(s.Properties, "protocol", "auto", "h1", "h2", "h2c", "h3")
	}
	if t == reflect.TypeFor[IdentityPatch]() {
		setEnum(s.Properties, "client_ip_source", "peer", "trusted_x_forwarded_for", "trusted_forwarded")
		setEnum(s.Properties, "scheme_source", "connection", "trusted_x_forwarded_proto", "trusted_forwarded")
	}
	if t == reflect.TypeFor[ClientIPHeader]() {
		setEnum(s.Properties, "format", "single", "xff", "forwarded")
	}
	if t == reflect.TypeFor[HeaderMutation]() || t == reflect.TypeFor[QueryMutation]() {
		setEnum(s.Properties, "operation", "set", "add", "delete")
	}
	if t == reflect.TypeFor[CookieMutation]() {
		setEnum(s.Properties, "operation", "set", "delete")
	}
	return s
}

func setEnum(properties map[string]ParameterSchema, name string, values ...string) {
	s := properties[name]
	s.Enum = values
	properties[name] = s
}

func stageAllowed(stage Stage, stages []Stage) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}
