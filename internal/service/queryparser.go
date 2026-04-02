// queryparser.go transforms natural-language query strings into structured
// ParsedQuery values that can drive both the graph repository's QueryFilter
// and direct Cypher execution against Apache AGE. It handles:
//   - Keyword extraction with stop-word filtering.
//   - Time expression detection (e.g. "last week", "today", "recently").
//   - Bounds clamping for maxDepth (1..5) and topK (1..20).
//   - Cypher query construction for memory matching and neighborhood expansion.

package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/pkg/model"
)

// escapeCypherQ escapes single quotes in a string to prevent Cypher injection
// when interpolating user-provided values into query templates.
func escapeCypherQ(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// ParsedQuery represents a structured query extracted from a natural-language
// search string. It captures the parsed keywords, optional time filter, memory
// type constraints, and pagination parameters needed by downstream retrieval.
type ParsedQuery struct {
	Keywords  []string
	Tags      []string
	Types     []model.MemoryType
	TimeAfter *time.Time
	ProjectID string
	MaxDepth  int32
	TopK      int32
}

// ParseQuery converts a raw query string and request parameters into a structured
// ParsedQuery. It extracts keywords (filtering stop words), detects and removes
// time expressions (setting TimeAfter accordingly), and clamps maxDepth and topK
// to safe bounds.
func ParseQuery(query string, projectID string, types []model.MemoryType, maxDepth, topK int32) ParsedQuery {
	keywords := extractKeywords(query)

	var timeAfter *time.Time
	remaining := filterTimeKeywords(query, &timeAfter)
	if remaining != query {
		keywords = extractKeywords(remaining)
	}

	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 5 {
		maxDepth = 5
	}
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	return ParsedQuery{
		Keywords:  keywords,
		Types:     types,
		TimeAfter: timeAfter,
		ProjectID: projectID,
		MaxDepth:  maxDepth,
		TopK:      topK,
	}
}

// ToGraphFilter converts a ParsedQuery into the graph.QueryFilter struct expected
// by the repository layer. Note that TimeAfter is not yet propagated to the filter
// and is handled separately by the Cypher query builder.
func (p ParsedQuery) ToGraphFilter() graph.QueryFilter {
	return graph.QueryFilter{
		ProjectID: p.ProjectID,
		Keywords:  p.Keywords,
		Tags:      p.Tags,
		Types:     p.Types,
		MaxDepth:  p.MaxDepth,
		TopK:      p.TopK,
	}
}

// filterTimeKeywords scans query for time expressions and sets timeAfter.
// Returns the query with time expressions removed.
func filterTimeKeywords(query string, timeAfter **time.Time) string {
	now := time.Now().UTC()
	lower := strings.ToLower(query)

	timePatterns := []struct {
		keyword  string
		duration time.Duration
	}{
		{"today", 24 * time.Hour},
		{"yesterday", 48 * time.Hour},
		{"last hour", time.Hour},
		{"last day", 24 * time.Hour},
		{"last week", 7 * 24 * time.Hour},
		{"this week", 7 * 24 * time.Hour},
		{"last month", 30 * 24 * time.Hour},
		{"this month", 30 * 24 * time.Hour},
		{"recent", 3 * 24 * time.Hour},
		{"recently", 3 * 24 * time.Hour},
	}

	for _, p := range timePatterns {
		if strings.Contains(lower, p.keyword) {
			t := now.Add(-p.duration)
			*timeAfter = &t
			// Remove the time keyword from the query.
			idx := strings.Index(lower, p.keyword)
			query = query[:idx] + query[idx+len(p.keyword):]
			break
		}
	}

	return strings.TrimSpace(query)
}

// BuildCypherQuery constructs a Cypher MATCH query from a ParsedQuery for direct
// execution against Apache AGE. The generated query filters by project_id, optional
// memory types, optional time range, and keyword matching (case-insensitive
// substring search against both content and tags). It fetches 3x the requested topK
// (minimum 20) to give the ranking layer enough candidates for re-ranking.
func BuildCypherQuery(p ParsedQuery) string {
	var conditions []string
	conditions = append(conditions, fmt.Sprintf("m.project_id = '%s'", escapeCypherQ(p.ProjectID)))

	if len(p.Types) > 0 {
		var typeStrs []string
		for _, t := range p.Types {
			typeStrs = append(typeStrs, fmt.Sprintf("'%s'", string(t)))
		}
		conditions = append(conditions, fmt.Sprintf("m.type IN [%s]", strings.Join(typeStrs, ", ")))
	}

	if p.TimeAfter != nil {
		conditions = append(conditions, fmt.Sprintf("m.created_at >= '%s'", p.TimeAfter.Format(time.RFC3339)))
	}

	if len(p.Keywords) > 0 {
		var kwConds []string
		for _, kw := range p.Keywords {
			kw = escapeCypherQ(strings.ToLower(kw))
			kwConds = append(kwConds, fmt.Sprintf("(toLower(m.content) CONTAINS '%s' OR toLower(m.tags) CONTAINS '%s')", kw, kw))
		}
		conditions = append(conditions, "("+strings.Join(kwConds, " OR ")+")")
	}

	where := strings.Join(conditions, " AND ")

	// Fetch more than topK to allow re-ranking.
	fetchLimit := p.TopK * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}

	return fmt.Sprintf(
		`MATCH (m:Memory) WHERE %s RETURN properties(m) ORDER BY m.created_at DESC LIMIT %d`,
		where,
		fetchLimit,
	)
}

// BuildNeighborhoodQuery returns a Cypher query that expands 1-hop neighbors of
// a given memory node. If edgeTypes is non-empty, only edges matching those
// relationship types are traversed; otherwise all edge types are included.
func BuildNeighborhoodQuery(memoryID string, edgeTypes []model.RelationshipType) string {
	if len(edgeTypes) == 0 {
		return fmt.Sprintf(
			`MATCH (m:Memory {id: '%s'})-[e]-(neighbor:Memory) RETURN properties(neighbor), type(e), properties(e)`,
			escapeCypherQ(memoryID),
		)
	}

	var labels []string
	for _, et := range edgeTypes {
		labels = append(labels, string(et))
	}

	return fmt.Sprintf(
		`MATCH (m:Memory {id: '%s'})-[e:%s]-(neighbor:Memory) RETURN properties(neighbor), type(e), properties(e)`,
		escapeCypherQ(memoryID),
		strings.Join(labels, "|"),
	)
}
