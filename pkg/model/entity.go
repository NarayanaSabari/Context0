// entity.go defines the Entity node type: a person, place, organisation or
// work that memories are about.
//
// Entities exist because the graph previously linked memory to memory by
// embedding similarity, which clusters paraphrases of one fact rather than
// connecting things to each other. Multi-hop questions were the weakest LoCoMo
// category at 65% precisely because a question needing two hops had nothing to
// traverse: `Caroline` was never connected to her dog `Biscuit`, only to other
// sentences that happened to be worded like the one mentioning him.
//
// Modelling an entity as a node rather than as a tag is what makes the hop
// possible. A tag is a string on a memory, so two memories sharing one are
// only findable by scanning for that string; a shared node is one edge apart
// in either direction.

package model

import (
	"strings"

	"github.com/google/uuid"
)

// Entity is a thing memories are about: a person, animal, place,
// organisation, product, event or work.
//
// Deliberately not a memory. An entity holds no content, is never returned by
// a query on its own, and carries no decay or access history -- it exists to
// connect memories, and giving it the properties of a memory would put it in
// competition with them in ranking.
type Entity struct {
	// ID is the unique identifier for this entity node.
	ID uuid.UUID `json:"id"`

	// Name is the entity as it was first written, e.g. "New York". Kept for
	// display; never for matching.
	Name string `json:"name"`

	// NormalizedName is what identity is decided on: lowercased, trimmed, with
	// any possessive suffix removed. `Biscuit`, `biscuit` and `Biscuit's` all
	// resolve here, which is what makes two memories mentioning the same thing
	// reach one node rather than three.
	NormalizedName string `json:"normalized_name"`

	// ProjectID scopes the entity to a project. Entities are not shared across
	// projects: the project is the tenant boundary everywhere else in this
	// engine, and an entity node spanning two of them would make one tenant's
	// memories reachable from another's.
	ProjectID string `json:"project_id"`
}

// RelMentions is the edge from a memory to an entity it names.
//
// A separate relationship rather than reusing relates_to, because the two
// answer different questions and a traversal has to be able to tell them
// apart: relates_to connects memories that resemble each other, mentions
// connects a memory to a thing in the world. Collapsing them would make
// "what else is about Biscuit?" indistinguishable from "what else reads like
// this sentence?".
const RelMentions RelationshipType = "mentions"

// NormalizeEntity is the key two mentions of one entity must agree on.
//
// It lives in the domain package because two layers have to agree on it
// exactly: extraction produces entity names, and the repository decides node
// identity by MERGEing on this value. Any drift between them would create a
// second node for an entity that already exists, which is precisely the
// linking failure entities were introduced to fix.
//
// Lowercased, with surrounding punctuation and a possessive suffix removed, so
// `Biscuit`, `biscuit` and `Biscuit's` resolve to one node rather than three.
// Internal spacing is collapsed, because "New  York" and "New York" are the
// same place.
//
// Deliberately shallow beyond that. Stemming would merge `Baker` and `Bakers`,
// which are different names, and this value decides which memories are treated
// as being about the same thing.
func NormalizeEntity(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Trim(name, ".,;:!?\"'\u201c\u201d()[]{}")
	name = strings.TrimSuffix(name, "'s")
	name = strings.TrimSuffix(name, "\u2019s")
	return strings.Join(strings.Fields(name), " ")
}
