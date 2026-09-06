package main

import (
	"math"
	"net/http"
	"sort"
	"strings"
)

// RoleStats summarises one role's population and clock-skew posture.
type RoleStats struct {
	Role             string  `json:"role"`
	NodeCount        int     `json:"nodeCount"`
	WithSkew         int     `json:"withSkew"`
	MeanAbsSkewSec   float64 `json:"meanAbsSkewSec"`
	MedianAbsSkewSec float64 `json:"medianAbsSkewSec"`
	OkCount          int     `json:"okCount"`
	WarningCount     int     `json:"warningCount"`
	CriticalCount    int     `json:"criticalCount"`
	AbsurdCount      int     `json:"absurdCount"`
	NoClockCount     int     `json:"noClockCount"`
}

// RoleAnalyticsResponse is the payload returned by /api/analytics/roles.
type RoleAnalyticsResponse struct {
	TotalNodes int         `json:"totalNodes"`
	Roles      []RoleStats `json:"roles"`
}

// normalizeRole canonicalises a role string so empty/unknown roles bucket
// together and case differences don't fragment the distribution.
func normalizeRole(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	if r == "" {
		return "unknown"
	}
	return r
}

// computeRoleAnalytics groups nodes by role and aggregates clock-skew per
// role. Pure function: takes the node roster and the per-pubkey skew map and
// returns the response — no store / lock dependencies, easy to unit test.
//
// `nodesByPubkey` lists every known node (pubkey → role). `skewByPubkey`
// is the subset of pubkeys that have clock-skew data with their severity and
// most-recent corrected skew (in seconds, signed — we take |x| for averages).
func computeRoleAnalytics(nodesByPubkey map[string]string, skewByPubkey map[string]*NodeClockSkew) RoleAnalyticsResponse {
	type bucket struct {
		stats    RoleStats
		absSkews []float64
	}
	buckets := make(map[string]*bucket)
	for pk, rawRole := range nodesByPubkey {
		role := normalizeRole(rawRole)
		b, ok := buckets[role]
		if !ok {
			b = &bucket{stats: RoleStats{Role: role}}
			buckets[role] = b
		}
		b.stats.NodeCount++
		cs, has := skewByPubkey[pk]
		if !has || cs == nil {
			continue
		}
		b.stats.WithSkew++
		abs := math.Abs(cs.RecentMedianSkewSec)
		if abs == 0 {
			abs = math.Abs(cs.LastSkewSec)
		}
		b.absSkews = append(b.absSkews, abs)
		switch cs.Severity {
		case SkewOK:
			b.stats.OkCount++
		case SkewWarning:
			b.stats.WarningCount++
		case SkewCritical:
			b.stats.CriticalCount++
		case SkewAbsurd:
			b.stats.AbsurdCount++
		case SkewNoClock:
			b.stats.NoClockCount++
		}
	}
	resp := RoleAnalyticsResponse{Roles: make([]RoleStats, 0, len(buckets))}
	for _, b := range buckets {
		if n := len(b.absSkews); n > 0 {
			sum := 0.0
			for _, v := range b.absSkews {
				sum += v
			}
			b.stats.MeanAbsSkewSec = round(sum/float64(n), 2)
			sorted := make([]float64, n)
			copy(sorted, b.absSkews)
			sort.Float64s(sorted)
			if n%2 == 1 {
				b.stats.MedianAbsSkewSec = round(sorted[n/2], 2)
			} else {
				b.stats.MedianAbsSkewSec = round((sorted[n/2-1]+sorted[n/2])/2, 2)
			}
		}
		resp.TotalNodes += b.stats.NodeCount
		resp.Roles = append(resp.Roles, b.stats)
	}
	// Sort: largest population first, then role name for stable output.
	sort.Slice(resp.Roles, func(i, j int) bool {
		if resp.Roles[i].NodeCount != resp.Roles[j].NodeCount {
			return resp.Roles[i].NodeCount > resp.Roles[j].NodeCount
		}
		return resp.Roles[i].Role < resp.Roles[j].Role
	})
	return resp
}

// handleAnalyticsRoles serves /api/analytics/roles. Reads from the
// steady-state recomputer snapshot (issue #1256) so the request never
// holds s.mu.RLock for a full clock-skew recompute over the advert
// transmissions — that path hung >60s on staging with 78k tx.
func (s *Server) handleAnalyticsRoles(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, RoleAnalyticsResponse{Roles: []RoleStats{}})
		return
	}
	writeJSON(w, s.store.GetAnalyticsRoles())
}

// GetAnalyticsRoles returns the role-distribution analytics, preferring
// the steady-state recomputer snapshot (issue #1256). Falls back to an
// on-request compute path if the recomputer is not yet running (e.g.
// during the brief startup window before the initial compute completes
// — Start runs it synchronously, so this fallback is effectively only
// hit in tests that skip the recomputer entirely).
func (s *PacketStore) GetAnalyticsRoles() RoleAnalyticsResponse {
	s.analyticsRecomputerMu.RLock()
	rc := s.recompRoles
	s.analyticsRecomputerMu.RUnlock()
	if rc != nil {
		if v := rc.Load(); v != nil {
			if r, ok := v.(RoleAnalyticsResponse); ok {
				s.cacheMu.Lock()
				s.cacheHits++
				s.cacheMu.Unlock()
				return r
			}
		}
	}
	return s.computeAnalyticsRoles()
}

// computeAnalyticsRoles runs the actual role aggregation. Used by the
// background recomputer (issue #1256) and as a fallback for callers
// arriving before the snapshot is populated.
func (s *PacketStore) computeAnalyticsRoles() RoleAnalyticsResponse {
	nodes, _ := s.getCachedNodesAndPM()
	roles := make(map[string]string, len(nodes))
	for _, n := range nodes {
		roles[n.PublicKey] = n.Role
	}
	skewMap := make(map[string]*NodeClockSkew)
	for _, cs := range s.GetFleetClockSkew("") {
		if cs == nil {
			continue
		}
		skewMap[cs.Pubkey] = cs
	}
	return computeRoleAnalytics(roles, skewMap)
}
