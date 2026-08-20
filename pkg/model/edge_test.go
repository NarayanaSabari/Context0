package model

import "testing"

// RelationshipType.Valid is the only thing that makes edge-label
// interpolation safe.
//
// A relationship becomes a Cypher edge label, and openCypher has no parameter
// slot for labels, so the value is necessarily interpolated into the query
// text. Membership in this closed set is what stops an arbitrary string
// closing the pattern and appending clauses. Nothing in this package had a
// test.

func TestValidAcceptsExactlyTheDefinedTypes(t *testing.T) {
	defined := []RelationshipType{
		RelBelongsTo, RelContains, RelRelatesTo, RelSupersedes, RelCausedBy,
	}
	for _, r := range defined {
		if !r.Valid() {
			t.Errorf("%q is a defined relationship but Valid() rejected it; "+
				"a legitimate edge cannot be created", r)
		}
	}

	// The set must be exactly these. A new type added to the constants but not
	// to the map would be rejected at runtime with no clue why, and one added
	// to the map but never defined would be silently accepted.
	if len(relationshipTypes) != len(defined) {
		t.Errorf("relationshipTypes holds %d entries but %d are defined; "+
			"the map and the constants have drifted apart",
			len(relationshipTypes), len(defined))
	}
}

func TestValidRejectsAnythingElse(t *testing.T) {
	// The first two are real injection attempts: each closes the edge pattern
	// and appends its own clause. Confirmed against a live database -- both are
	// refused before reaching Cypher, and no node is created.
	rejected := []RelationshipType{
		"relates_to]->(x) CREATE (z:Memory {id:'INJECTED'}) MERGE (a)-[q:relates_to",
		"relates_to`]->(b) DETACH DELETE a //",
		"relates_to' + '",
		"",
		" ",
		"relates_to ",
		" relates_to",
		"RELATES_TO", // case matters: the label is written verbatim
		"Relates_To",
		"relates-to", // hyphen, not underscore
		"relatesto",
		"unknown",
		"*",
	}
	for _, r := range rejected {
		if r.Valid() {
			t.Errorf("Valid() accepted %q; this value is interpolated into a "+
				"Cypher edge label, so anything outside the closed set can "+
				"close the pattern and append clauses", r)
		}
	}
}

// TestValidIsCaseSensitive is called out separately because it is the
// surprising one: the label is written into the query exactly as given, so
// "RELATES_TO" would produce a different edge type in the graph, not the same
// one. Accepting it would silently create edges no traversal looks for.
func TestValidIsCaseSensitive(t *testing.T) {
	if RelationshipType("RELATES_TO").Valid() {
		t.Error("Valid() accepted an upper-case spelling; the label is written " +
			"verbatim, so this would create an edge type nothing queries for")
	}
	if !RelRelatesTo.Valid() {
		t.Error("the canonical spelling was rejected")
	}
}
