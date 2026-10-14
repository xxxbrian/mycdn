package rules

import (
	"encoding/json"
	"net/http"
	"testing"
)

func str(s string) *string { return &s }

func TestOrderedBranchesUseStageEntryFacts(t *testing.T) {
	rules := []Rule{{ID: "request", Enabled: true, Stage: Request, Steps: []Step{
		{ID: "rewrite", Type: "action", Action: &Action{Type: "path", Path: str("/changed")}},
		{ID: "if", Type: "if", Branches: []Branch{
			{ID: "changed", Condition: &Condition{Type: "match", Field: "path", Op: "eq", Values: []string{"/changed"}}, Steps: []Step{{ID: "bad", Type: "action", Action: &Action{Type: "request_header", Header: &HeaderMutation{Operation: "set", Name: "X-Branch", Value: "bad"}}}}},
			{ID: "original", Condition: &Condition{Type: "all", Children: []Condition{{Type: "match", Field: "path", Op: "prefix", Values: []string{"/old"}}, {Type: "not", Children: []Condition{{Type: "match", Field: "method", Op: "eq", Values: []string{"POST"}}}}}}, Steps: []Step{{ID: "good", Type: "action", Action: &Action{Type: "request_header", Header: &HeaderMutation{Operation: "set", Name: "X-Branch", Value: "good"}}}}},
			{ID: "else", Steps: []Step{{ID: "else-action", Type: "action", Action: &Action{Type: "origin", Origin: str("other")}}}},
		}},
		{ID: "independent", Type: "if", Branches: []Branch{{ID: "all", Condition: &Condition{Type: "all"}, Steps: []Step{{ID: "last", Type: "action", Action: &Action{Type: "request_header", Header: &HeaderMutation{Operation: "set", Name: "X-Last", Value: "yes"}}}}}}},
	}}}
	program, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	plans := Plans{Fetch: FetchPlan{Path: "/old/file", Header: make(http.Header)}}
	var trace []Trace
	program.Run(Request, Facts{Path: "/old/file", Method: "GET"}, &plans, &trace)
	if plans.Fetch.Path != "/changed" || plans.Fetch.Header.Get("X-Branch") != "good" || plans.Fetch.Header.Get("X-Last") != "yes" || plans.Fetch.OriginID != "" {
		t.Fatalf("incorrect ordered plan: %+v", plans)
	}
	if len(trace) != 6 {
		t.Fatalf("trace = %+v", trace)
	}
	// Compiled rule payloads do not alias the caller's document.
	*rules[0].Steps[0].Action.Path = "/mutated"
	program.Run(Request, Facts{Path: "/old/file", Method: "GET"}, &plans, nil)
	if plans.Fetch.Path != "/changed" {
		t.Fatal("compiled program retained mutable input")
	}
}

func TestCIDRHeaderAndTerminal(t *testing.T) {
	program, err := Compile([]Rule{{ID: "ingress", Enabled: true, Stage: Ingress, Steps: []Step{{ID: "if", Type: "if", Branches: []Branch{{ID: "match", Condition: &Condition{Type: "all", Children: []Condition{
		{Type: "match", Field: "peer_ip", Op: "cidr", Values: []string{"192.0.2.0/24"}},
		{Type: "match", Field: "header", Key: "x-test", Op: "in", Values: []string{"One", "Two"}},
		{Type: "match", Field: "path", Op: "regex", Values: []string{`^/files/[a-z]+\.txt$`}},
	}}, Steps: []Step{{ID: "reply", Type: "action", Action: &Action{Type: "reply", Reply: &Reply{Status: 403, Body: "no"}}}, {ID: "unreachable", Type: "action", Action: &Action{Type: "response_header", Header: &HeaderMutation{Operation: "set", Name: "X-Bad", Value: "yes"}}}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	plans := Plans{}
	program.Run(Ingress, Facts{PeerIP: "192.0.2.9", Path: "/files/test.txt", Header: http.Header{"X-Test": {"One"}}}, &plans, nil)
	if plans.Terminal == nil || plans.Terminal.Status != 403 || len(plans.Delivery.Headers) != 0 {
		t.Fatalf("terminal behavior = %+v", plans)
	}
}

func TestValidationRejectsUnavailableAndProtocolOwnedEdits(t *testing.T) {
	cookie := HeaderMutation{Operation: "delete", Name: "Set-Cookie"}
	if ValidateHeader(cookie, "source_header") == nil || ValidateHeader(cookie, "response_header") != nil {
		t.Fatal("cookie edits must be delivery-only so storage eligibility sees the original header")
	}
	for _, action := range []Action{
		{Type: "request_header", Header: &HeaderMutation{Operation: "set", Name: "Content-Length", Value: "1"}},
		{Type: "source_header", Header: &HeaderMutation{Operation: "delete", Name: "ETag"}},
		{Type: "request_header", Header: &HeaderMutation{Operation: "set", Name: "X-Test", Value: "bad\r\nInjected: x"}},
		{Type: "origin", Origin: str("one"), Path: str("/two")},
	} {
		if err := ValidateAction(action, Request); err == nil {
			t.Fatalf("accepted %+v", action)
		}
	}
	_, err := Compile([]Rule{{ID: "bad", Enabled: false, Stage: Request, Steps: []Step{{ID: "if", Type: "if", Branches: []Branch{{ID: "status", Condition: &Condition{Type: "match", Field: "status", Op: "eq", Values: []string{"200"}}}}}}}})
	if err == nil {
		t.Fatal("accepted response field in request stage of disabled rule")
	}
}

func TestCapabilitiesDescribeTypedActionParameters(t *testing.T) {
	c := Capabilities()
	for _, action := range c.Actions {
		schema, ok := c.Schemas[action.Parameter]
		if !ok || schema.Type == "" {
			t.Fatalf("missing schema for %+v", action)
		}
	}
	limits := c.Schemas["limits"]
	if len(limits.Required) != 0 || limits.Properties["allow_upgrade"].Type != "boolean" {
		t.Fatalf("patch schema = %+v", limits)
	}
	key := c.Schemas["cache_key"]
	if len(key.Properties["query_mode"].Enum) != 4 {
		t.Fatalf("cache key enum = %+v", key)
	}
}

func TestSourceCaptureRetainsOnlyImmutableRuleDependencies(t *testing.T) {
	program, err := Compile([]Rule{{ID: "source", Enabled: true, Stage: OriginResponse, Steps: []Step{{ID: "if", Type: "if", Branches: []Branch{{ID: "match", Condition: &Condition{Type: "all", Children: []Condition{{Type: "match", Field: "original.header", Key: "X-Partition", Op: "eq", Values: []string{"one"}}, {Type: "match", Field: "status", Op: "eq", Values: []string{"200"}}}}, Steps: []Step{{ID: "header", Type: "action", Action: &Action{Type: "source_header", Header: &HeaderMutation{Operation: "set", Name: "X-Matched", Value: "yes"}}}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	original := Facts{Header: http.Header{"X-Partition": {"one"}, "X-Request-Id": {"consumer"}}}
	facts := Facts{ClientIP: "192.0.2.8", Header: original.Header.Clone(), Original: &original}
	captured := program.CaptureFacts(OriginResponse, facts)
	if captured.ClientIP != "" || captured.Header != nil || captured.Original != nil || len(captured.captured) != 1 {
		t.Fatalf("consumer metadata retained: %+v", captured)
	}
	if program.FactsKey(OriginResponse, facts) != program.FactsKey(OriginResponse, captured) {
		t.Fatal("captured dependency identity differs")
	}
	original.Header.Set("X-Partition", "mutated")
	captured.Status = 200
	plans := Plans{Content: ContentPlan{ResponseHeader: make(http.Header)}}
	program.Run(OriginResponse, captured, &plans, nil)
	if plans.Content.ResponseHeader.Get("X-Matched") != "yes" {
		t.Fatal("capture did not own rule dependency")
	}
}

func TestIngressUsesPeerAndHTTPVersionNotDerivedClientIP(t *testing.T) {
	for _, field := range []string{"client_ip", "original.client_ip"} {
		_, err := Compile([]Rule{{ID: "ingress", Enabled: true, Stage: Ingress, Steps: []Step{{ID: "if", Type: "if", Branches: []Branch{{ID: "match", Condition: &Condition{Type: "match", Field: field, Op: "eq", Values: []string{"192.0.2.9"}}}}}}}})
		if err == nil {
			t.Fatalf("accepted circular ingress field %q", field)
		}
	}
	program, err := Compile([]Rule{{ID: "ingress", Enabled: true, Stage: Ingress, Steps: []Step{{ID: "if", Type: "if", Branches: []Branch{{ID: "match", Condition: &Condition{Type: "all", Children: []Condition{{Type: "match", Field: "peer_ip", Op: "cidr", Values: []string{"10.0.0.0/8"}}, {Type: "match", Field: "http_version", Op: "eq", Values: []string{"HTTP/2.0"}}}}, Steps: []Step{{ID: "select", Type: "action", Action: &Action{Type: "identity", Identity: &IdentityPatch{ClientIPHeaders: &[]ClientIPHeader{{Name: "X-Real-IP", Format: "single"}}}}}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	plans := Plans{}
	program.Run(Ingress, Facts{PeerIP: "10.1.2.3", HTTPVersion: "HTTP/2.0"}, &plans, nil)
	if len(plans.Request.Identity.ClientIPHeaders) != 1 {
		t.Fatalf("ingress did not select custom source: %+v", plans.Request.Identity)
	}
	copy := plans.Clone()
	copy.Request.Identity.ClientIPHeaders[0].Name = "X-Mutated"
	if plans.Request.Identity.ClientIPHeaders[0].Name != "X-Real-IP" {
		t.Fatal("identity list aliases copied plan")
	}
	for _, source := range []ClientIPHeader{{Name: "bad header", Format: "single"}, {Name: "X-Real-IP", Format: "guess"}} {
		if err := ValidateIdentityPatch(IdentityPatch{ClientIPHeaders: &[]ClientIPHeader{source}}); err == nil {
			t.Fatalf("accepted source %+v", source)
		}
	}
}

func TestIdentityPatchPreservesExplicitEmptyList(t *testing.T) {
	var action Action
	if err := json.Unmarshal([]byte(`{"type":"identity","identity":{"client_ip_headers":[]}}`), &action); err != nil {
		t.Fatal(err)
	}
	program, err := Compile([]Rule{{ID: "identity", Enabled: true, Stage: Ingress, Steps: []Step{{ID: "peer", Type: "action", Action: &action}}}})
	if err != nil {
		t.Fatal(err)
	}
	plans := Plans{Request: RequestPlan{Identity: IdentitySettings{ClientIPHeaders: []ClientIPHeader{{Name: "X-Real-IP", Format: "single"}}}}}
	program.Run(Ingress, Facts{}, &plans, nil)
	if list := plans.Request.Identity.ClientIPHeaders; list == nil || len(list) != 0 {
		t.Fatalf("explicit empty list = %+v", list)
	}
	catalog := Capabilities()
	schema := catalog.Schemas["identity"].Properties["client_ip_headers"]
	if schema.Type != "array" || schema.Items == nil || len(schema.Items.Properties["format"].Enum) != 3 {
		t.Fatalf("identity list schema = %+v", schema)
	}
}
