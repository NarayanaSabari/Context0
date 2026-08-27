package graph

// Integration tests for entity nodes and the mentions edges connecting
// memories to them. Real Cypher against a real Apache AGE instance, because
// the claim is about what the graph can reach and a mock reaches whatever it
// likes.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// entityNodeCount reports how many Entity vertices a project holds.
func entityNodeCount(t *testing.T, repo *AGERepository, ctx context.Context, projectID, normalized string) int {
	t.Helper()
	rows, err := repo.cypher(ctx,
		`MATCH (e:Entity) WHERE e.project_id = $p AND e.normalized_name = $n RETURN e.id`,
		params{"p": projectID, "n": normalized})
	if err != nil {
		t.Fatalf("count entity nodes: %v", err)
	}
	ids, err := scanAgtype[string](rows)
	if err != nil {
		t.Fatalf("scan entity nodes: %v", err)
	}
	return len(ids)
}

// storeLinkedMemory creates a memory and links it to the given entities.
func storeLinkedMemory(t *testing.T, repo *AGERepository, ctx context.Context, projectID, content string, entities ...string) model.Memory {
	t.Helper()
	mem := storeMemory(t, repo, ctx, newMemory(projectID, content))
	if _, err := repo.LinkEntities(ctx, mem, entities); err != nil {
		t.Fatalf("link entities: %v", err)
	}
	return mem
}

// TestLinkEntities_IsSafeUnderConcurrency is the test this feature most needed.
//
// AGE's MERGE is not safe against concurrent writers, and entity linking is
// the hottest place in the engine for that: a conversation's memories are
// written in a loop and several conversations about the same person arrive at
// once, so every writer races for one node.
//
// Measured before the advisory lock, with 12 concurrent calls naming one
// entity: six failed with `Entity failed to be updated: 3 (SQLSTATE XX000)`,
// two duplicate `biscuit` nodes were created, and only 6 of 12 memories were
// reachable afterwards. Silently, since the service layer logs a link failure
// and moves on -- which is exactly the failure entities exist to prevent.
func TestLinkEntities_IsSafeUnderConcurrency(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	const writers = 12
	mems := make([]model.Memory, 0, writers)
	for i := 0; i < writers; i++ {
		mems = append(mems, storeMemory(t, repo, ctx,
			newMemory(projectID, fmt.Sprintf("memory number %d about the dog", i))))
	}

	// Released together, so the writers genuinely contend rather than queueing
	// behind each other's completed writes.
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for _, mem := range mems {
		wg.Add(1)
		go func(mem model.Memory) {
			defer wg.Done()
			start.Wait()
			if _, err := repo.LinkEntities(ctx, mem, []string{"Biscuit"}); err != nil {
				errs <- err
			}
		}(mem)
	}
	start.Done()
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a concurrent entity link failed: %v", err)
	}

	if n := entityNodeCount(t, repo, ctx, projectID, "biscuit"); n != 1 {
		t.Errorf("%d `biscuit` entity nodes exist, want 1: concurrent writers "+
			"created their own instead of attaching to one, so the memories are "+
			"partitioned across nodes and cannot reach each other", n)
	}

	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Biscuit"}, 100)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != writers {
		t.Errorf("%d of %d memories are reachable through the entity; a memory "+
			"whose link was lost is invisible to every multi-hop query", len(found), writers)
	}
}

// Re-linking the same memory to the same entity must not pile up nodes or
// edges. Extract can legitimately run twice over one transcript, and the graph
// is what pays for it.
func TestLinkEntities_IsIdempotent(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "Caroline adopted a rescue dog named Biscuit."))

	names := []string{"Caroline", "Biscuit"}
	for i := 0; i < 3; i++ {
		if _, err := repo.LinkEntities(ctx, mem, names); err != nil {
			t.Fatalf("link entities (pass %d): %v", i, err)
		}
	}

	got, err := repo.GetMemoryEntities(ctx, []uuid.UUID{mem.ID})
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	if len(got[mem.ID]) != len(names) {
		t.Errorf("after 3 identical passes the memory has %d entity edges (%v), want %d",
			len(got[mem.ID]), got[mem.ID], len(names))
	}
	if n := entityNodeCount(t, repo, ctx, projectID, "biscuit"); n != 1 {
		t.Errorf("%d `biscuit` nodes exist after 3 identical passes, want 1", n)
	}
}

// Variant spellings must reach one node, or the memories mentioning them are
// not connected and the feature is inert while appearing to work.
func TestLinkEntities_ResolvesVariantSpellingsToOneNode(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i, spelling := range []string{"Biscuit", "biscuit", "Biscuit's", "BISCUIT"} {
		storeLinkedMemory(t, repo, ctx, projectID,
			fmt.Sprintf("memory %d about the dog", i), spelling)
	}

	if n := entityNodeCount(t, repo, ctx, projectID, "biscuit"); n != 1 {
		t.Errorf("%d entity nodes for one dog, want 1: variant spellings must "+
			"resolve to one node", n)
	}

	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Biscuit"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != 4 {
		t.Errorf("a lookup for `Biscuit` returned %d memories, want 4", len(found))
	}
}

// The project is the tenant boundary everywhere else in this engine. A shared
// entity node would make one tenant's memories reachable from another's in a
// single hop, which is a harder failure to notice than a wrong ranking.
func TestEntities_DoNotCrossProjects(t *testing.T) {
	repo, ctx := testRepo(t)
	projectA, projectB := newProjectID(t), newProjectID(t)

	inA := storeLinkedMemory(t, repo, ctx, projectA, "the dog is terrified of thunderstorms", "Biscuit")
	storeLinkedMemory(t, repo, ctx, projectB, "the dog won the county show", "Biscuit")

	found, err := repo.FindMemoriesByEntities(ctx, projectA, []string{"Biscuit"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != 1 || found[0].ID != inA.ID {
		t.Errorf("a lookup in project A returned %d memories; entities must not "+
			"connect tenants", len(found))
	}
	for _, m := range found {
		if m.ProjectID != projectA {
			t.Errorf("a memory from project %s leaked into project %s's traversal",
				m.ProjectID, projectA)
		}
	}

	// Each project holds its own node, rather than one shared node that
	// happens to be filtered on read.
	if n := entityNodeCount(t, repo, ctx, projectA, "biscuit"); n != 1 {
		t.Errorf("project A has %d `biscuit` nodes, want 1", n)
	}
	if n := entityNodeCount(t, repo, ctx, projectB, "biscuit"); n != 1 {
		t.Errorf("project B has %d `biscuit` nodes, want 1", n)
	}
}

// An entity mentioned by most of a project would return the whole corpus if
// the traversal were unbounded, which is a full table scan wearing a
// traversal's clothes.
func TestFindMemoriesByEntities_RespectsTheLimit(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i := 0; i < 12; i++ {
		storeLinkedMemory(t, repo, ctx, projectID, fmt.Sprintf("memory %d", i), "Caroline")
	}

	const limit = 5
	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Caroline"}, limit)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != limit {
		t.Errorf("a lookup with limit %d returned %d memories", limit, len(found))
	}
}

// A memory naming two of the query's entities matches the pattern twice and
// must still be returned once, or it would consume two candidate slots.
func TestFindMemoriesByEntities_ReturnsEachMemoryOnce(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	storeLinkedMemory(t, repo, ctx, projectID,
		"Caroline adopted a rescue dog named Biscuit", "Caroline", "Biscuit")

	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Caroline", "Biscuit"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("a memory naming both query entities was returned %d times, want once", len(found))
	}
}

// Empty inputs must be no-ops rather than matching everything, which is the
// shape of bug that turns a scoped traversal into a full scan.
func TestFindMemoriesByEntities_EmptyInputsAreNoOps(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)
	storeLinkedMemory(t, repo, ctx, projectID, "a memory about Caroline", "Caroline")

	for _, tc := range []struct {
		name    string
		project string
		names   []string
		limit   int
	}{
		{"no names", projectID, nil, 10},
		{"blank names", projectID, []string{"", "  ", "."}, 10},
		{"zero limit", projectID, []string{"Caroline"}, 0},
		{"negative limit", projectID, []string{"Caroline"}, -1},
	} {
		got, err := repo.FindMemoriesByEntities(ctx, tc.project, tc.names, tc.limit)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: returned %d memories, want none", tc.name, len(got))
		}
	}
}

// Entity names come from conversation text, which is caller-controlled. They
// are bound as parameters rather than interpolated, and this is what proves
// it: a name shaped like a Cypher injection must be stored as a name.
func TestLinkEntities_TreatsHostileNamesAsData(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "a memory with hostile entities"))

	hostile := []string{
		`x'}) CREATE (z:Memory {id:'INJECTED', content:'INJECTED'}) //`,
		`y"}) DETACH DELETE m //`,
		"back\\slash and 'quotes'",
		"newline\nand\ttab",
	}
	if _, err := repo.LinkEntities(ctx, mem, hostile); err != nil {
		t.Fatalf("link hostile entities: %v", err)
	}

	results, err := repo.QueryMemories(ctx, QueryFilter{Keywords: []string{"INJECTED"}, TopK: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) > 0 {
		t.Fatalf("an injected memory reached the graph: %+v", results[0].Memory)
	}

	// And the memory still exists, so no DETACH DELETE ran either.
	if _, err := repo.GetMemory(ctx, mem.ID); err != nil {
		t.Errorf("the memory was deleted by an injected entity name: %v", err)
	}

	// The hostile strings round-trip as ordinary names.
	got, err := repo.GetMemoryEntities(ctx, []uuid.UUID{mem.ID})
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	if len(got[mem.ID]) != len(hostile) {
		t.Errorf("stored %d hostile names, got %d back (%v)",
			len(hostile), len(got[mem.ID]), got[mem.ID])
	}
}

// GetMemoryEntities is read on the ranking path for every result at once, so
// it must handle the whole batch and the empty case without a round trip per
// memory.
func TestGetMemoryEntities_HandlesBatchesAndEmptyInput(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	withEntities := storeLinkedMemory(t, repo, ctx, projectID, "a memory about the dog", "Biscuit")
	withoutEntities := storeMemory(t, repo, ctx, newMemory(projectID, "a memory naming nothing at all"))

	got, err := repo.GetMemoryEntities(ctx, []uuid.UUID{withEntities.ID, withoutEntities.ID})
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	if names := got[withEntities.ID]; len(names) != 1 || names[0] != "biscuit" {
		t.Errorf("entities for the linked memory = %v, want [biscuit]", names)
	}
	// A memory with no entities must be absent rather than present with an
	// empty slice, so ranking can tell "names nothing" from "not looked up".
	if names, ok := got[withoutEntities.ID]; ok {
		t.Errorf("a memory naming nothing appeared in the result as %v", names)
	}

	empty, err := repo.GetMemoryEntities(ctx, nil)
	if err != nil {
		t.Fatalf("get memory entities for no ids: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("an empty lookup returned %d entries", len(empty))
	}
}

// A name that normalises to nothing is not an entity, and creating a node for
// it would give every such memory a shared hub connecting unrelated facts.
func TestLinkEntities_SkipsNamesThatNormaliseToNothing(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "a memory"))

	linked, err := repo.LinkEntities(ctx, mem, []string{"", "   ", "...", "'"})
	if err != nil {
		t.Fatalf("link entities: %v", err)
	}
	if linked != 0 {
		t.Errorf("linked %d entities for names that normalise to nothing", linked)
	}
	if n := entityNodeCount(t, repo, ctx, projectID, ""); n != 0 {
		t.Errorf("%d entity nodes exist with an empty name", n)
	}
}

// A memory with no project cannot own entities: the project is what scopes
// them, and a nil-scoped node would be reachable from every tenant.
func TestLinkEntities_RefusesAMemoryWithNoProject(t *testing.T) {
	repo, ctx := testRepo(t)

	mem := newMemory("", "a memory with no project")
	linked, err := repo.LinkEntities(ctx, mem, []string{"Biscuit"})
	if err != nil {
		t.Fatalf("link entities: %v", err)
	}
	if linked != 0 {
		t.Errorf("linked %d entities to a memory with no project", linked)
	}
}

// The same name in different cases must take the same advisory lock, or the
// serialisation that makes concurrent linking safe would not apply to exactly
// the case it exists for.
func TestLinkEntities_VariantSpellingsContendForOneNode(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	spellings := []string{"Biscuit", "biscuit", "BISCUIT", "Biscuit's", "  Biscuit  ", "biscuit."}
	mems := make([]model.Memory, 0, len(spellings))
	for i := range spellings {
		mems = append(mems, storeMemory(t, repo, ctx,
			newMemory(projectID, fmt.Sprintf("memory %d about the dog", i))))
	}

	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	errs := make(chan error, len(spellings))
	for i, spelling := range spellings {
		wg.Add(1)
		go func(mem model.Memory, spelling string) {
			defer wg.Done()
			start.Wait()
			if _, err := repo.LinkEntities(ctx, mem, []string{spelling}); err != nil {
				errs <- err
			}
		}(mems[i], spelling)
	}
	start.Done()
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a concurrent link with a variant spelling failed: %v", err)
	}
	if n := entityNodeCount(t, repo, ctx, projectID, "biscuit"); n != 1 {
		t.Errorf("%d `biscuit` nodes after concurrent variant spellings, want 1: "+
			"the lock must be taken on the normalized name, not the raw one", n)
	}
}

// isConcurrentUpdate decides what gets retried, and it matches on AGE's error
// text because AGE reports it as XX000 (internal_error), which is far too
// broad to retry on alone.
func TestIsConcurrentUpdate_MatchesOnlyAGEsConcurrencyError(t *testing.T) {
	retryable := fmt.Errorf("ERROR: Entity failed to be updated: 3 (SQLSTATE XX000)")
	if !isConcurrentUpdate(retryable) {
		t.Error("AGE's concurrent-update error was not recognised; entity linking " +
			"would fail under contention rather than retrying")
	}

	for _, err := range []error{
		nil,
		fmt.Errorf("connection refused"),
		fmt.Errorf("ERROR: syntax error at or near \"MERGE\""),
		fmt.Errorf("ERROR: relation does not exist"),
	} {
		if isConcurrentUpdate(err) {
			t.Errorf("retrying %v; only AGE's concurrency error is safe to repeat, "+
				"because everything else will fail identically three times", err)
		}
	}
}

// Deleting a memory must take with it any entity it was the last mention of.
//
// DETACH DELETE removes the mentions edges but not the entities, so without
// this a store that deletes and re-ingests -- which consolidation's prune
// phase does continuously -- accumulates nodes nothing points at.
func TestDeleteMemory_RemovesEntitiesItWasTheLastMentionOf(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	shared := storeLinkedMemory(t, repo, ctx, projectID, "Caroline walks the dog", "Caroline", "Biscuit")
	soleMention := storeLinkedMemory(t, repo, ctx, projectID, "Caroline reads by the fire", "Caroline", "Marlow")

	if err := repo.DeleteMemory(ctx, soleMention.ID); err != nil {
		t.Fatalf("delete memory: %v", err)
	}

	// Marlow had exactly one mention, which is now gone.
	if n := entityNodeCount(t, repo, ctx, projectID, "marlow"); n != 0 {
		t.Errorf("%d `marlow` nodes remain after its only mention was deleted; "+
			"orphaned entities accumulate for the life of the store", n)
	}

	// Caroline still has one, and must survive: deleting a still-referenced
	// entity would silently disconnect the memories that name it.
	if n := entityNodeCount(t, repo, ctx, projectID, "caroline"); n != 1 {
		t.Errorf("%d `caroline` nodes remain, want 1: an entity another memory "+
			"still mentions must not be collected", n)
	}
	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Caroline"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != 1 || found[0].ID != shared.ID {
		t.Errorf("the surviving memory is no longer reachable through Caroline "+
			"(got %d results)", len(found))
	}
}

// A memory naming several of the query's entities must not consume several of
// the limit's slots, or the memories that match the query best are exactly the
// ones pushed out.
func TestFindMemoriesByEntities_DeduplicatesBeforeApplyingTheLimit(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// One memory naming both query entities, and two naming one each. With a
	// limit of 2 and no DISTINCT, the double-matching memory eats both slots
	// and one single-matching memory is lost inside the database, where no
	// amount of Go-side deduplication can recover it.
	both := storeLinkedMemory(t, repo, ctx, projectID, "Caroline walks Biscuit", "Caroline", "Biscuit")
	first := storeLinkedMemory(t, repo, ctx, projectID, "Caroline reads by the fire", "Caroline")
	second := storeLinkedMemory(t, repo, ctx, projectID, "Biscuit sleeps under the table", "Biscuit")

	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Caroline", "Biscuit"}, 2)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) != 2 {
		var ids []string
		for _, m := range found {
			ids = append(ids, m.Content)
		}
		t.Fatalf("a limit of 2 returned %d distinct memories (%v); a memory "+
			"matching twice must not consume two slots", len(found), ids)
	}

	// Whichever two came back must be distinct memories from the three stored.
	seen := map[uuid.UUID]bool{}
	for _, m := range found {
		if seen[m.ID] {
			t.Errorf("memory %s was returned twice", m.ID)
		}
		seen[m.ID] = true
		if m.ID != both.ID && m.ID != first.ID && m.ID != second.ID {
			t.Errorf("unexpected memory %s in the results", m.ID)
		}
	}
}

// The memory's own project is checked, not just the entity's.
//
// The id and the project arrive in the same model.Memory value but from
// different places, so a caller passing a memory id from one project with
// another project's id would otherwise attach a project-B entity to a
// project-A memory. Both sides are filtered on read, so the result is silent
// graph corruption rather than a leak -- which is the kind that survives.
func TestLinkEntities_RefusesToLinkAcrossProjects(t *testing.T) {
	repo, ctx := testRepo(t)
	projectA, projectB := newProjectID(t), newProjectID(t)

	inA := storeMemory(t, repo, ctx, newMemory(projectA, "a memory belonging to project A"))

	// The same memory, mislabelled with project B.
	mislabelled := inA
	mislabelled.ProjectID = projectB

	if _, err := repo.LinkEntities(ctx, mislabelled, []string{"Biscuit"}); err != nil {
		t.Fatalf("link entities: %v", err)
	}

	got, err := repo.GetMemoryEntities(ctx, []uuid.UUID{inA.ID})
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	if len(got[inA.ID]) != 0 {
		t.Errorf("a project-A memory was linked to a project-B entity (%v); the "+
			"memory's own project must be checked, not only the entity's", got[inA.ID])
	}
}
