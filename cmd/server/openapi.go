package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
)

// routeMeta holds metadata for a single API route.
type routeMeta struct {
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Tag         string   `json:"tag"`
	Auth        bool     `json:"auth,omitempty"`
	QueryParams []paramMeta `json:"queryParams,omitempty"`
}

type paramMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type"` // "string", "integer", "boolean"
}

// routeDescriptions returns metadata for all known API routes.
// Key format: "METHOD /path/pattern"
func routeDescriptions() map[string]routeMeta {
	return map[string]routeMeta{
		// Config
		"GET /api/config/cache":      {Summary: "Get cache configuration", Tag: "config"},
		"GET /api/config/client":     {Summary: "Get client configuration", Tag: "config"},
		"GET /api/config/regions":    {Summary: "Get configured regions", Tag: "config"},
		"GET /api/config/theme":      {Summary: "Get theme configuration", Description: "Returns color maps, CSS variables, and theme defaults.", Tag: "config"},
		"GET /api/config/map":        {Summary: "Get map configuration", Tag: "config"},
		"GET /api/config/geo-filter": {Summary: "Get geo-filter configuration", Tag: "config"},

		// Admin / system
		"GET /api/health":     {Summary: "Health check", Description: "Returns server health, uptime, and memory stats.", Tag: "admin"},
		"GET /api/stats":      {Summary: "Network statistics", Description: "Returns aggregate stats (node counts, packet counts, observer counts). Cached for 10s.", Tag: "admin"},
		"GET /api/perf":       {Summary: "Performance statistics", Description: "Returns per-endpoint request timing and slow query log.", Tag: "admin"},
		"POST /api/perf/reset": {Summary: "Reset performance stats", Tag: "admin", Auth: true},
		"POST /api/admin/prune": {Summary: "Prune old data", Description: "Deletes packets and nodes older than the configured retention period.", Tag: "admin", Auth: true},
		"GET /api/debug/affinity": {Summary: "Debug neighbor affinity scores", Tag: "admin", Auth: true},

		// Packets
		"GET /api/packets": {Summary: "List packets", Description: "Returns decoded packets with filtering, sorting, and pagination.", Tag: "packets",
			QueryParams: []paramMeta{
				{Name: "limit", Description: "Max packets to return", Type: "integer"},
				{Name: "offset", Description: "Pagination offset", Type: "integer"},
				{Name: "sort", Description: "Sort field", Type: "string"},
				{Name: "order", Description: "Sort order (asc/desc)", Type: "string"},
				{Name: "type", Description: "Filter by packet type", Type: "string"},
				{Name: "observer", Description: "Filter by observer ID", Type: "string"},
				{Name: "timeRange", Description: "Time range filter (e.g. 1h, 24h, 7d)", Type: "string"},
				{Name: "search", Description: "Full-text search", Type: "string"},
				{Name: "groupByHash", Description: "Group duplicate packets by hash", Type: "boolean"},
			}},
		"POST /api/packets":              {Summary: "Ingest a packet", Description: "Submit a raw packet for decoding and storage.", Tag: "packets", Auth: true},
		"GET /api/packets/{id}":          {Summary: "Get packet detail", Tag: "packets"},
		"GET /api/packets/timestamps":    {Summary: "Get packet timestamp ranges", Tag: "packets"},
		"POST /api/packets/observations": {Summary: "Batch submit observations", Description: "Submit multiple observer sightings for existing packets.", Tag: "packets"},

		// Decode
		"POST /api/decode": {Summary: "Decode a raw packet", Description: "Decodes a hex-encoded packet without storing it.", Tag: "packets"},

		// Nodes
		"GET /api/nodes": {Summary: "List nodes", Description: "Returns all known mesh nodes with status and metadata.", Tag: "nodes",
			QueryParams: []paramMeta{
				{Name: "role", Description: "Filter by node role", Type: "string"},
				{Name: "status", Description: "Filter by status (active/stale/offline)", Type: "string"},
			}},
		"GET /api/nodes/search":           {Summary: "Search nodes", Description: "Search nodes by name or public key prefix.", Tag: "nodes", QueryParams: []paramMeta{{Name: "q", Description: "Search query", Type: "string", Required: true}}},
		"GET /api/nodes/bulk-health":       {Summary: "Bulk node health", Description: "Returns health status for all nodes in one call.", Tag: "nodes"},
		"GET /api/nodes/network-status":    {Summary: "Network status summary", Description: "Returns counts of active, stale, and offline nodes.", Tag: "nodes"},
		"GET /api/nodes/{pubkey}":          {Summary: "Get node detail", Description: "Returns full detail for a single node by public key.", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/health":   {Summary: "Get node health", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/paths":    {Summary: "Get node routing paths", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/analytics": {Summary: "Get node analytics", Description: "Per-node packet counts, timing, and RF stats.", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/neighbors": {Summary: "Get node neighbors", Description: "Returns neighbor nodes with affinity scores.", Tag: "nodes"},

		// Analytics
		"GET /api/analytics/rf":               {Summary: "RF analytics", Description: "SNR/RSSI distributions and statistics.", Tag: "analytics"},
		"GET /api/analytics/topology":          {Summary: "Network topology", Description: "Hop-count distribution and route analysis.", Tag: "analytics"},
		"GET /api/analytics/channels":          {Summary: "Channel analytics", Description: "Message counts and activity per channel.", Tag: "analytics"},
		"GET /api/analytics/distance":          {Summary: "Distance analytics", Description: "Geographic distance calculations between nodes.", Tag: "analytics"},
		"GET /api/analytics/hash-sizes":        {Summary: "Hash size analysis", Description: "Distribution of hash prefix sizes across the network.", Tag: "analytics"},
		"GET /api/analytics/hash-collisions":   {Summary: "Hash collision detection", Description: "Identifies nodes sharing hash prefixes.", Tag: "analytics"},
		"GET /api/analytics/subpaths":          {Summary: "Subpath analysis", Description: "Common routing subpaths through the mesh.", Tag: "analytics"},
		"GET /api/analytics/subpaths-bulk":     {Summary: "Bulk subpath analysis", Tag: "analytics"},
		"GET /api/analytics/subpath-detail":    {Summary: "Subpath detail", Tag: "analytics"},
		"GET /api/analytics/neighbor-graph":    {Summary: "Neighbor graph", Description: "Full neighbor affinity graph for visualization.", Tag: "analytics"},

		// Channels
		"GET /api/channels":                 {Summary: "List channels", Description: "Returns known mesh channels with message counts.", Tag: "channels"},
		"GET /api/channels/{hash}/messages": {Summary: "Get channel messages", Description: "Returns messages for a specific channel.", Tag: "channels"},

		// Observers
		"GET /api/observers":                    {Summary: "List observers", Description: "Returns all known packet observers/gateways.", Tag: "observers"},
		"GET /api/observers/{id}":               {Summary: "Get observer detail", Tag: "observers"},
		"GET /api/observers/{id}/metrics":       {Summary: "Get observer metrics", Description: "Packet rates, uptime, and performance metrics.", Tag: "observers"},
		"GET /api/observers/{id}/analytics":     {Summary: "Get observer analytics", Tag: "observers"},
		"GET /api/observers/metrics/summary":    {Summary: "Observer metrics summary", Description: "Aggregate metrics across all observers.", Tag: "observers"},

		// Misc
		"GET /api/resolve-hops":      {Summary: "Resolve hop path", Description: "Resolves hash prefixes in a hop path to node names. Returns affinity scores and best candidates.", Tag: "nodes", QueryParams: []paramMeta{{Name: "hops", Description: "Comma-separated hop hash prefixes", Type: "string", Required: true}}},
		"GET /api/traces/{hash}":     {Summary: "Get packet traces", Description: "Returns all observer sightings for a packet hash.", Tag: "packets"},
		"GET /api/iata-coords":       {Summary: "Get IATA airport coordinates", Description: "Returns lat/lon for known airport codes (used for observer positioning).", Tag: "config"},
		"GET /api/audio-lab/buckets": {Summary: "Audio lab frequency buckets", Description: "Returns frequency bucket data for audio analysis.", Tag: "analytics"},
	}
}

// buildOpenAPISpec constructs an OpenAPI 3.0 spec by walking the mux router.
func buildOpenAPISpec(router *mux.Router, version string) map[string]interface{} {
	descriptions := routeDescriptions()

	// Collect routes from the router
	type routeInfo struct {
		path    string
		method  string
		authReq bool
	}
	var routes []routeInfo

	router.Walk(func(route *mux.Route, router *mux.Router, ancestors []*mux.Route) error {
		path, err := route.GetPathTemplate()
		if err != nil {
			return nil
		}
		if !strings.HasPrefix(path, "/api/") {
			return nil
		}
		// Skip the spec/docs endpoints themselves
		if path == "/api/spec" || path == "/api/docs" {
			return nil
		}
		methods, err := route.GetMethods()
		if err != nil {
			return nil
		}
		for _, m := range methods {
			routes = append(routes, routeInfo{path: path, method: m})
		}
		return nil
	})

	// Sort routes for deterministic output
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})

	// Build paths object
	paths := make(map[string]interface{})
	tagSet := make(map[string]bool)

	for _, ri := range routes {
		key := ri.method + " " + ri.path
		meta, hasMeta := descriptions[key]

		// Convert mux path params {name} to OpenAPI {name} (same format, convenient)
		openAPIPath := ri.path

		// Build operation
		op := map[string]interface{}{
			"summary": func() string {
				if hasMeta {
					return meta.Summary
				}
				return ri.path
			}(),
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "Success",
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"type": "object"},
						},
					},
				},
			},
		}

		if hasMeta {
			if meta.Description != "" {
				op["description"] = meta.Description
			}
			if meta.Tag != "" {
				op["tags"] = []string{meta.Tag}
				tagSet[meta.Tag] = true
			}
			if meta.Auth {
				op["security"] = []map[string]interface{}{
					{"ApiKeyAuth": []string{}},
				}
			}

			// Add query parameters
			if len(meta.QueryParams) > 0 {
				params := make([]interface{}, 0, len(meta.QueryParams))
				for _, qp := range meta.QueryParams {
					p := map[string]interface{}{
						"name":     qp.Name,
						"in":       "query",
						"required": qp.Required,
						"schema":   map[string]interface{}{"type": qp.Type},
					}
					if qp.Description != "" {
						p["description"] = qp.Description
					}
					params = append(params, p)
				}
				op["parameters"] = params
			}
		}

		// Extract path parameters from {name} patterns
		pathParams := extractPathParams(openAPIPath)
		if len(pathParams) > 0 {
			existing, _ := op["parameters"].([]interface{})
			for _, pp := range pathParams {
				existing = append(existing, map[string]interface{}{
					"name":     pp,
					"in":       "path",
					"required": true,
					"schema":   map[string]interface{}{"type": "string"},
				})
			}
			op["parameters"] = existing
		}

		// Add to paths
		methodLower := strings.ToLower(ri.method)
		if _, ok := paths[openAPIPath]; !ok {
			paths[openAPIPath] = make(map[string]interface{})
		}
		paths[openAPIPath].(map[string]interface{})[methodLower] = op
	}

	// Build tags array (sorted)
	tagOrder := []string{"admin", "analytics", "channels", "config", "nodes", "observers", "packets"}
	tagDescriptions := map[string]string{
		"admin":     "Server administration and diagnostics",
		"analytics": "Network analytics and statistics",
		"channels":  "Mesh channel operations",
		"config":    "Server configuration",
		"nodes":     "Mesh node operations",
		"observers": "Packet observer/gateway operations",
		"packets":   "Packet capture and decoding",
	}
	var tags []interface{}
	for _, t := range tagOrder {
		if tagSet[t] {
			tags = append(tags, map[string]interface{}{
				"name":        t,
				"description": tagDescriptions[t],
			})
		}
	}

	spec := map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "CoreScope API",
			"description": "MeshCore network analyzer — packet capture, node tracking, and mesh analytics.",
			"version":     version,
			"license": map[string]interface{}{
				"name": "MIT",
			},
		},
		"paths": paths,
		"tags":  tags,
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{
				"ApiKeyAuth": map[string]interface{}{
					"type": "apiKey",
					"in":   "header",
					"name": "X-API-Key",
				},
			},
		},
	}

	return spec
}

// extractPathParams returns parameter names from a mux-style path like /api/nodes/{pubkey}.
func extractPathParams(path string) []string {
	var params []string
	for {
		start := strings.Index(path, "{")
		if start == -1 {
			break
		}
		end := strings.Index(path[start:], "}")
		if end == -1 {
			break
		}
		params = append(params, path[start+1:start+end])
		path = path[start+end+1:]
	}
	return params
}

// handleOpenAPISpec serves the OpenAPI 3.0 spec as JSON.
// The router is injected via RegisterRoutes storing it on the Server.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	spec := buildOpenAPISpec(s.router, s.version)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode spec: %v", err), http.StatusInternalServerError)
	}
}

// handleSwaggerUI serves a minimal Swagger UI page.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, swaggerUIHTML)
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>CoreScope API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    html { box-sizing: border-box; overflow-y: scroll; }
    *, *:before, *:after { box-sizing: inherit; }
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: '/api/spec',
      dom_id: '#swagger-ui',
      deepLinking: true,
      presets: [
        SwaggerUIBundle.presets.apis,
        SwaggerUIBundle.SwaggerUIStandalonePreset
      ],
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>`
