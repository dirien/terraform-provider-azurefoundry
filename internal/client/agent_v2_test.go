// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// decodeFirstTool re-marshals payload.Definition.Tools[0] to JSON and
// decodes it as a generic map. Foundry decodes Tools as []any, so this is
// the same path the wire goes through — we just inspect it after the fact.
func decodeFirstTool(t *testing.T, payload CreateAgentV2Request) (decoded map[string]any, raw []byte) {
	t.Helper()
	if len(payload.Definition.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(payload.Definition.Tools))
	}
	raw, err := json.Marshal(payload.Definition.Tools[0])
	if err != nil {
		t.Fatalf("re-marshaling tool: %v", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding tool: %v", err)
	}
	return decoded, raw
}

func makeAgentV2EchoResponse(toolJSON []byte, name string) string {
	return `{"object":"agent","id":"asst_abc","name":"` + name + `","versions":{"latest":{"id":"v1","name":"` +
		name + `","version":"1","description":"","created_at":1700000000,"definition":{"kind":"prompt","tools":[` +
		string(toolJSON) + `]}}}}`
}

// TestCreateAgentV2_MCPHeadersAndAllowedToolsRoundTrip pins the wire shape
// for the mcp tool block's allowed_tools + headers fields (issue #8). The
// schema accepts these on agent_v2 and toolbox_v2; both share the same
// extractor/wirer so verifying the wire body once covers both paths.
func TestCreateAgentV2_MCPHeadersAndAllowedToolsRoundTrip(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var payload CreateAgentV2Request
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		mcp, raw := decodeFirstTool(t, payload)
		assertMCPHeaderShape(t, mcp)
		return newProbeResponse(http.StatusOK, makeAgentV2EchoResponse(raw, "sharepoint-kb")), nil
	})

	c := newTestClient(rt)
	resp, err := c.CreateAgentV2(context.Background(), CreateAgentV2Request{
		Name: "sharepoint-kb",
		Definition: AgentDefinitionV2{
			Kind: "prompt",
			Tools: []any{
				MCPToolV2{
					Type:                "mcp",
					ServerLabel:         "knowledge-base",
					ServerURL:           "https://x.search.windows.net/knowledgebases/k/mcp?api-version=2025-11-01-preview",
					ProjectConnectionID: "kb-conn",
					RequireApproval:     "never",
					AllowedTools:        []string{"knowledge_base_retrieve"},
					Headers: map[string]string{
						"x-ms-query-source-authorization": "Bearer xyz",
						"Foundry-Features":                "Toolboxes=V1Preview",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentV2: %v", err)
	}
	assertMCPEchoRoundTrip(t, resp.Versions.Latest.Definition.Tools)
}

// assertMCPHeaderShape verifies the inbound JSON for an mcp tool with
// allowed_tools + headers. Split out so the test body stays under the
// gocyclo budget.
func assertMCPHeaderShape(t *testing.T, mcp map[string]any) {
	t.Helper()
	if mcp["type"] != "mcp" {
		t.Errorf("expected type=mcp, got %v", mcp["type"])
	}
	allowed, _ := mcp["allowed_tools"].([]any)
	if len(allowed) != 1 || allowed[0] != "knowledge_base_retrieve" {
		t.Errorf("expected allowed_tools=[knowledge_base_retrieve], got %v", mcp["allowed_tools"])
	}
	headers, ok := mcp["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected headers map, got %v (%T)", mcp["headers"], mcp["headers"])
	}
	if headers["x-ms-query-source-authorization"] != "Bearer xyz" {
		t.Errorf("unexpected x-ms-query-source-authorization: %v", headers["x-ms-query-source-authorization"])
	}
	if headers["Foundry-Features"] != "Toolboxes=V1Preview" {
		t.Errorf("unexpected Foundry-Features: %v", headers["Foundry-Features"])
	}
}

func assertMCPEchoRoundTrip(t *testing.T, tools []any) {
	t.Helper()
	if len(tools) != 1 {
		t.Fatalf("expected 1 echoed tool, got %d", len(tools))
	}
	rawEcho, _ := json.Marshal(tools[0])
	var echoed MCPToolV2
	if err := json.Unmarshal(rawEcho, &echoed); err != nil {
		t.Fatalf("decoding echoed tool: %v", err)
	}
	if len(echoed.AllowedTools) != 1 || echoed.AllowedTools[0] != "knowledge_base_retrieve" {
		t.Errorf("AllowedTools didn't round-trip: %v", echoed.AllowedTools)
	}
	if len(echoed.Headers) != 2 || echoed.Headers["Foundry-Features"] != "Toolboxes=V1Preview" {
		t.Errorf("Headers didn't round-trip: %v", echoed.Headers)
	}
}

// TestCreateAgentV2_OpenAPIHeadersRoundTrip pins the wire shape for the
// new openapi.headers field (issue #8). Headers sit on the outer tool
// envelope (sibling to "openapi"), matching how Foundry positions the
// field for mcp.
func TestCreateAgentV2_OpenAPIHeadersRoundTrip(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var payload CreateAgentV2Request
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		openapi, raw := decodeFirstTool(t, payload)
		assertOpenAPIHeaderShape(t, openapi)
		return newProbeResponse(http.StatusOK, makeAgentV2EchoResponse(raw, "weather")), nil
	})

	c := newTestClient(rt)
	resp, err := c.CreateAgentV2(context.Background(), CreateAgentV2Request{
		Name: "weather",
		Definition: AgentDefinitionV2{
			Kind: "prompt",
			Tools: []any{
				OpenAPIToolV2{
					Type: "openapi",
					OpenAPI: OpenAPIConfig{
						Name: "weather",
						Spec: map[string]any{"openapi": "3.0.0"},
						Auth: OpenAPIAuth{Type: "anonymous"},
					},
					Headers: map[string]string{"x-tenant-id": "t-1"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentV2: %v", err)
	}

	rawEcho, _ := json.Marshal(resp.Versions.Latest.Definition.Tools[0])
	var echoed OpenAPIToolV2
	if err := json.Unmarshal(rawEcho, &echoed); err != nil {
		t.Fatalf("decoding echoed tool: %v", err)
	}
	if echoed.Headers["x-tenant-id"] != "t-1" {
		t.Errorf("OpenAPIToolV2.Headers didn't round-trip: %v", echoed.Headers)
	}
}

func assertOpenAPIHeaderShape(t *testing.T, openapi map[string]any) {
	t.Helper()
	if openapi["type"] != "openapi" {
		t.Errorf("expected type=openapi, got %v", openapi["type"])
	}
	headers, ok := openapi["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected headers map, got %v (%T)", openapi["headers"], openapi["headers"])
	}
	if headers["x-tenant-id"] != "t-1" {
		t.Errorf("unexpected x-tenant-id header: %v", headers["x-tenant-id"])
	}
	inner, ok := openapi["openapi"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested openapi envelope, got %v (%T)", openapi["openapi"], openapi["openapi"])
	}
	if inner["name"] != "weather" {
		t.Errorf("unexpected inner name: %v", inner["name"])
	}
}

// TestMCPToolV2_OmitsEmptyHeadersAndAllowedTools — when neither field is
// set, both must be omitted from the wire body so we don't introduce
// drift against existing agents that pre-date issue #8.
func TestMCPToolV2_OmitsEmptyHeadersAndAllowedTools(t *testing.T) {
	t.Parallel()

	tool := MCPToolV2{
		Type:        "mcp",
		ServerLabel: "x",
		ServerURL:   "https://x.example.com",
	}
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "allowed_tools") {
		t.Errorf("expected allowed_tools to be omitted when empty, got %s", got)
	}
	if strings.Contains(got, "headers") {
		t.Errorf("expected headers to be omitted when empty, got %s", got)
	}
}
