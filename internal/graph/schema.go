package graph

// GraphName is the Apache AGE graph used by Context0.
const GraphName = "context0"

// Schema initialization queries for Apache AGE.
// AGE uses label-based node/edge typing within a single graph.
var schemaQueries = []string{
	// Create the graph if it doesn't exist.
	// AGE requires this before any Cypher queries.
	`SELECT * FROM ag_catalog.create_graph('` + GraphName + `')`,

	// Create indexes on commonly queried properties.
	// AGE currently supports creating indexes on the underlying Postgres tables.
	// Vertex label tables are: <graph_name>.<label>
	// For MVP, we rely on AGE's internal indexing and add explicit indexes as needed.
}

// dropGraphQuery drops the entire graph (used in tests).
const dropGraphQuery = `SELECT * FROM ag_catalog.drop_graph('` + GraphName + `', true)`
