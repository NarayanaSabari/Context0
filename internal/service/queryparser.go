package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/pkg/model"
)

// escapeCypherQ escapes single quotes for Cypher strings.
func escapeCypherQ(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// ParsedQuery represents a structured query extracted from natural language.
type ParsedQuery struct {
	Keywords  []string
	Tags      []string
	Types     []model.MemoryType
	TimeAfter *time.Time
	ProjectID string
	MaxDepth  int32
	TopK      int32
}

// ParseQuery converts a QueryRequest into a structured ParsedQuery.
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

// ToGraphFilter converts a ParsedQuery into a graph.QueryFilter.
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
			lower = strings.ToLower(query)
			break
		}
	}

	return strings.TrimSpace(query)
}

// BuildCypherQuery constructs a Cypher query string from a ParsedQuery.
// This is used for direct Cypher execution against AGE.
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

// BuildNeighborhoodQuery returns a Cypher query that expands 1-hop neighbors
// of a given memory node, optionally filtered by edge types.
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
