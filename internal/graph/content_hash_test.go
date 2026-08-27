package graph

// Integration tests for the content-hash deduplication support that
// write-time consolidation depends on. Same contract as the rest of this
// suite: real Cypher against a real Apache AGE instance, because the value is
// in catching queries that compile in Go and are malformed openCypher.

import (
	"context"
	"strings"
	"testing"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// semanticKey builds the lookup key for a semantic memory, which is what
// newMemory produces. FindByContentHash keys by type as well as content
// because the type is part of the claim: GetProfile splits its static and
// dynamic layers on exactly that field.
func semanticKey(hash string) ContentKey {
	return ContentKey{Hash: hash, Type: model.MemoryTypeSemantic}
}

// FindByContentHash is the cheap half of write-time consolidation: one query
// tells a whole conversation's worth of extracted memories which of them the
// project already holds.
func TestFindByContentHash_MatchesOnContentNotFormatting(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	stored := storeMemory(t, repo, ctx, newMemory(projectID, "Caroline is transgender."))

	// Each of these is the same fact written differently. The hash normalises
	// case, punctuation and spacing and nothing else, so all four must find
	// the stored row.
	for _, variant := range []string{
		"Caroline is transgender.",
		"caroline is transgender",
		"  Caroline   is  transgender!  ",
		"CAROLINE IS TRANSGENDER",
	} {
		hash := model.ContentHash(variant)
		found, err := repo.FindByContentHash(ctx, projectID, []string{hash})
		if err != nil {
			t.Fatalf("find by content hash %q: %v", variant, err)
		}
		got, ok := found[semanticKey(hash)]
		if !ok {
			t.Errorf("%q did not find the stored memory; the write path would "+
				"store a second copy of a fact it already holds", variant)
			continue
		}
		if got.ID != stored.ID {
			t.Errorf("%q found memory %s, want %s", variant, got.ID, stored.ID)
		}
	}
}

// The other direction. A false match here is destructive: the write path skips
// storing the memory entirely, so a fact that was never stored is silently
// lost.
func TestFindByContentHash_DoesNotMatchDifferentFacts(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	storeMemory(t, repo, ctx, newMemory(projectID, "Caroline is transgender."))

	for _, other := range []string{
		"Caroline is not transgender.",
		"Caroline is a transgender person.",
		"Melanie is transgender.",
	} {
		hash := model.ContentHash(other)
		found, err := repo.FindByContentHash(ctx, projectID, []string{hash})
		if err != nil {
			t.Fatalf("find by content hash: %v", err)
		}
		if _, ok := found[semanticKey(hash)]; ok {
			t.Errorf("%q matched a different fact; the write path would discard it "+
				"as a duplicate and the fact would never be stored", other)
		}
	}
}

// The project is the tenant boundary everywhere else in this repository, and
// deduplication must not be the one place it leaks: folding a new project's
// memory into another project's row would make one tenant's write disappear
// into another's data.
func TestFindByContentHash_IsScopedToTheProject(t *testing.T) {
	repo, ctx := testRepo(t)
	projectA, projectB := newProjectID(t), newProjectID(t)

	const content = "Caroline adopted a rescue dog named Biscuit."
	storeMemory(t, repo, ctx, newMemory(projectA, content))

	hash := model.ContentHash(content)
	found, err := repo.FindByContentHash(ctx, projectB, []string{hash})
	if err != nil {
		t.Fatalf("find by content hash: %v", err)
	}
	if _, ok := found[semanticKey(hash)]; ok {
		t.Error("a memory in one project matched a lookup scoped to another; " +
			"deduplication must respect the tenant boundary")
	}

	// And the same lookup in the owning project still works, so the scoping is
	// not just refusing everything.
	found, err = repo.FindByContentHash(ctx, projectA, []string{hash})
	if err != nil {
		t.Fatalf("find by content hash in owning project: %v", err)
	}
	if _, ok := found[semanticKey(hash)]; !ok {
		t.Error("the owning project did not find its own memory")
	}
}

// One query for the whole batch is the entire point: a lookup per memory would
// put a round trip on the write path for every extracted fact.
func TestFindByContentHash_ResolvesManyHashesAtOnce(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	contents := []string{
		"Caroline adopted a rescue dog named Biscuit.",
		"Melanie says the tax filing deadline is in April.",
		"Caroline works as a nurse at the county hospital.",
	}
	want := make(map[string]uuid.UUID, len(contents))
	hashes := make([]string, 0, len(contents)+1)
	for _, c := range contents {
		mem := storeMemory(t, repo, ctx, newMemory(projectID, c))
		h := model.ContentHash(c)
		want[h] = mem.ID
		hashes = append(hashes, h)
	}
	// An unstored hash in the same batch, so a match-everything implementation
	// cannot pass.
	absent := model.ContentHash("Caroline has never owned a cat.")
	hashes = append(hashes, absent)

	found, err := repo.FindByContentHash(ctx, projectID, hashes)
	if err != nil {
		t.Fatalf("find by content hash: %v", err)
	}

	for h, id := range want {
		got, ok := found[semanticKey(h)]
		if !ok {
			t.Errorf("hash %s was not resolved in a batch lookup", h)
			continue
		}
		if got.ID != id {
			t.Errorf("hash %s resolved to %s, want %s", h, got.ID, id)
		}
	}
	if _, ok := found[semanticKey(absent)]; ok {
		t.Error("a hash with no stored memory was resolved")
	}
}

// The hash is inlined into the Cypher text rather than bound as a parameter,
// for the reasons on uuidLiteralList, so the guard that makes that safe has to
// hold. These are real injection attempts: each closes the string literal and
// appends its own clause.
func TestFindByContentHash_RefusesValuesThatAreNotHashes(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	hostile := []string{
		"' RETURN 1 //",
		"deadbeefdeadbeefdeadbeefdeadbee'} CREATE (z:Memory {id:'INJECTED'}) //",
		"not-a-hash",
		strings.Repeat("z", 32),
	}

	for _, h := range hostile {
		if _, err := repo.FindByContentHash(ctx, projectID, []string{h}); err == nil {
			t.Errorf("FindByContentHash accepted %q; this value is written into a "+
				"Cypher string literal, so anything outside the hash alphabet can "+
				"close it and append clauses", h)
		}
	}

	// Nothing was created by the injection attempts.
	results, err := repo.QueryMemories(ctx, QueryFilter{Keywords: []string{"INJECTED"}, TopK: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) > 0 {
		t.Fatalf("an injected memory reached the graph: %+v", results[0].Memory)
	}
}

// An empty hash means content with no tokens at all, which is not a fact any
// lookup should resolve. Passing it through would make every such memory
// collide onto one bucket.
func TestFindByContentHash_EmptyInputsAreNoOps(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for _, tc := range []struct {
		name    string
		project string
		hashes  []string
	}{
		{"no hashes", projectID, nil},
		{"only empty hashes", projectID, []string{"", ""}},
		{"no project", "", []string{model.ContentHash("something")}},
	} {
		found, err := repo.FindByContentHash(ctx, tc.project, tc.hashes)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if len(found) != 0 {
			t.Errorf("%s: resolved %d memories, want none", tc.name, len(found))
		}
	}
}

// UpdateMemoryContent is what lets consolidation keep the fuller wording on a
// row that is already embedded and already linked, rather than storing a
// second row that says nearly the same thing.
func TestUpdateMemoryContent_RewritesTheHashWithTheContent(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	const before = "Caroline adopted a rescue dog."
	const after = "Caroline adopted a rescue dog named Biscuit."

	mem := storeMemory(t, repo, ctx, newMemory(projectID, before))

	updated, err := repo.UpdateMemoryContent(ctx, mem.ID, model.ContentHash(before), after)
	if err != nil {
		t.Fatalf("update memory content: %v", err)
	}
	if !updated {
		t.Fatal("the update did not apply against a row nothing else had touched")
	}

	got, err := repo.GetMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.Content != after {
		t.Errorf("content = %q, want %q", got.Content, after)
	}

	// The hash has to move with the content. Leaving it behind would make the
	// row findable only by text it no longer holds, so the next ingest of the
	// new wording would store a second copy -- exactly what consolidation
	// exists to prevent.
	newHash := model.ContentHash(after)
	found, err := repo.FindByContentHash(ctx, projectID, []string{newHash})
	if err != nil {
		t.Fatalf("find by new hash: %v", err)
	}
	if got, ok := found[semanticKey(newHash)]; !ok || got.ID != mem.ID {
		t.Errorf("the updated memory is not findable by the hash of its own content; "+
			"the next ingest of %q would store a duplicate", after)
	}

	// And it must no longer answer to the old content's hash, or the row would
	// keep absorbing writes of a fact it no longer states.
	oldHash := model.ContentHash(before)
	found, err = repo.FindByContentHash(ctx, projectID, []string{oldHash})
	if err != nil {
		t.Fatalf("find by old hash: %v", err)
	}
	if _, ok := found[semanticKey(oldHash)]; ok {
		t.Errorf("the memory still answers to the hash of %q, which it no longer says", before)
	}
}

// Rows written before content_hash existed carry no such property. They must
// stay invisible to hash lookups rather than colliding onto a single bucket,
// which is what an empty-string key would do.
func TestFindByContentHash_IgnoresRowsWrittenBeforeTheHashExisted(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// Written the way CreateMemory wrote rows before this feature: every
	// property except content_hash.
	legacyID := uuid.New()
	const q = `CREATE (m:Memory {id: $id, content: $content, type: $type, project_id: $project_id, ` +
		`tags: '[]', created_at: $created_at, access_count: 0, decay_score: 1.0}) RETURN m`
	if err := repo.cypherExec(ctx, q, params{
		"id":         legacyID.String(),
		"content":    "Caroline is transgender.",
		"type":       string(model.MemoryTypeSemantic),
		"project_id": projectID,
		"created_at": "2024-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatalf("create legacy memory: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteMemory(context.Background(), legacyID) })

	// A hash lookup for unrelated content must not return it. The failure mode
	// this guards against is a nil-hash row answering every lookup, which
	// would make the write path discard genuinely new facts.
	unrelated := model.ContentHash("Melanie says the tax deadline is in April.")
	found, err := repo.FindByContentHash(ctx, projectID, []string{unrelated})
	if err != nil {
		t.Fatalf("find by content hash: %v", err)
	}
	if _, ok := found[semanticKey(unrelated)]; ok {
		t.Error("a row written before content_hash existed matched an unrelated lookup")
	}
}

// The memory type is part of the claim, not metadata about it. GetProfile
// splits its static layer (semantic, procedural) from its dynamic one
// (episodic) on exactly that field, so folding an episodic memory into a
// semantic row with identical words would move a fact between the two layers.
func TestFindByContentHash_DoesNotMatchAcrossMemoryTypes(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	const content = "Caroline visited the coast."

	semantic := newMemory(projectID, content)
	storeMemory(t, repo, ctx, semantic)

	episodic := newMemory(projectID, content)
	episodic.Type = model.MemoryTypeEpisodic
	storeMemory(t, repo, ctx, episodic)

	hash := model.ContentHash(content)
	found, err := repo.FindByContentHash(ctx, projectID, []string{hash})
	if err != nil {
		t.Fatalf("find by content hash: %v", err)
	}

	for _, tc := range []struct {
		typ  model.MemoryType
		want uuid.UUID
	}{
		{model.MemoryTypeSemantic, semantic.ID},
		{model.MemoryTypeEpisodic, episodic.ID},
	} {
		got, ok := found[ContentKey{Hash: hash, Type: tc.typ}]
		if !ok {
			t.Errorf("no %s memory found for content stored as both types; "+
				"the write path would discard one of them as a duplicate of the other",
				tc.typ)
			continue
		}
		if got.ID != tc.want {
			t.Errorf("%s lookup returned %s, want %s: the two types resolved to "+
				"the same row", tc.typ, got.ID, tc.want)
		}
	}
}

// UpdateMemoryContent is a compare-and-set, and this is the case that makes
// that necessary.
//
// Consolidation reads a candidate, decides its own text subsumes it, and then
// writes. Two conversations can do that against the same row concurrently.
// Both see "Caroline adopted a dog"; if both writes land, the second erases
// the first's fact -- and that fact exists nowhere else, because consolidation
// skipped storing it precisely because it believed this row covered it.
func TestUpdateMemoryContent_RefusesToOverwriteAConcurrentWrite(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	const original = "Caroline adopted a dog."
	mem := storeMemory(t, repo, ctx, newMemory(projectID, original))

	// Both writers read the same content and computed the same guard.
	guard := model.ContentHash(original)

	first, err := repo.UpdateMemoryContent(ctx, mem.ID, guard, "Caroline adopted a rescue dog named Biscuit.")
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if !first {
		t.Fatal("the first update did not apply against an untouched row")
	}

	// The second writer's guard is now stale. It must be told so rather than
	// silently overwriting.
	second, err := repo.UpdateMemoryContent(ctx, mem.ID, guard, "Caroline adopted a small nervous dog.")
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if second {
		t.Error("a stale guard was accepted; the second write would have destroyed " +
			"a fact the first writer stored nowhere else")
	}

	got, err := repo.GetMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.Content != "Caroline adopted a rescue dog named Biscuit." {
		t.Errorf("content = %q; the first writer's text must survive", got.Content)
	}
}

// An unconditional overwrite is the failure this guard exists to prevent, so
// the API must not offer one. A caller with no hash to compare against is a
// caller that has not read the row.
func TestUpdateMemoryContent_RefusesAnUnguardedWrite(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "Caroline adopted a dog."))

	for _, guard := range []string{"", "not-a-hash", "' RETURN 1 //"} {
		updated, err := repo.UpdateMemoryContent(ctx, mem.ID, guard, "overwritten")
		if err == nil {
			t.Errorf("UpdateMemoryContent accepted guard %q; an unconditional "+
				"overwrite can destroy a concurrent write", guard)
		}
		if updated {
			t.Errorf("UpdateMemoryContent reported success for guard %q", guard)
		}
	}

	got, err := repo.GetMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.Content != "Caroline adopted a dog." {
		t.Errorf("content = %q; a refused update must change nothing", got.Content)
	}
}

// A memory that does not exist is not an error, but it is not an update
// either. Reporting it as applied would let a caller believe a fact is stored
// when nothing holds it.
func TestUpdateMemoryContent_ReportsAMissingMemoryAsNotUpdated(t *testing.T) {
	repo, ctx := testRepo(t)

	updated, err := repo.UpdateMemoryContent(ctx, uuid.New(),
		model.ContentHash("whatever it used to say"), "the new wording")
	if err != nil {
		t.Fatalf("update a missing memory: %v", err)
	}
	if updated {
		t.Error("updating a memory that does not exist reported success")
	}
}
