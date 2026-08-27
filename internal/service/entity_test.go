package service

// End-to-end tests for entity extraction and linking.
//
// The problem these pin: multi-hop questions were the weakest LoCoMo category
// at 65%, because the graph linked memory to memory by embedding similarity.
// That clusters paraphrases of one fact rather than connecting `Caroline` to
// her dog `Biscuit` to `thunderstorms`, so a question needing two hops had
// nothing to traverse.
//
// Everything here runs against a real database, because the claim is about
// what the graph can reach, and a mock graph can reach whatever it likes.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/google/uuid"
)

// extractMemoryIDs pulls the memory ids out of an extract response.
func extractMemoryIDs(t *testing.T, resp *pb.ExtractResponse) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, len(resp.Memories))
	for _, m := range resp.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestExtract_EntitiesAreStoredAsNodesNotTags is the first acceptance
// criterion.
//
// The distinction is not cosmetic. A tag is a string on a memory, so finding
// what else mentions it means scanning for that string; a node is one edge
// away in either direction, which is the whole reason the second hop becomes
// possible.
func TestExtract_EntitiesAreStoredAsNodesNotTags(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-nodes-%d", time.Now().UnixNano())

	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Caroline said that she adopted a rescue dog named Biscuit last month.",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	ids := extractMemoryIDs(t, resp)
	if len(ids) == 0 {
		t.Fatal("nothing was extracted")
	}

	byMemory, err := repo.GetMemoryEntities(ctx, ids)
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}

	var all []string
	for _, names := range byMemory {
		all = append(all, names...)
	}
	for _, want := range []string{"caroline", "biscuit"} {
		found := false
		for _, got := range all {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no entity node for %q; got %v. Entities must be nodes, not "+
				"tags, or there is nothing to traverse", want, all)
		}
	}

	// And they are genuinely separate from tags, which are topic labels from a
	// fixed vocabulary rather than things in the world.
	for _, m := range resp.Memories {
		for _, tag := range m.Tags {
			if strings.EqualFold(tag, "Biscuit") {
				t.Error("the entity was stored as a tag; a tag is a category this " +
					"engine recognises, an entity is a thing the world contains")
			}
		}
	}
}

// TestExtract_OneEntityMentionedTwiceResolvesToOneNode is the second
// acceptance criterion, with the deliberate paraphrase the issue asks for.
//
// If two mentions produce two nodes, the memories mentioning them are not
// connected at all and the whole feature is inert.
func TestExtract_OneEntityMentionedTwiceResolvesToOneNode(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-resolve-%d", time.Now().UnixNano())

	// Deliberate variation in how the name is written: possessive, plain, and
	// mid-sentence. All three must reach one node.
	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she adopted a rescue dog named Biscuit.\n" +
			"Caroline said that Biscuit's favourite walk is along the river.\n" +
			"Caroline said that she takes Biscuit to the vet every spring.",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	ids := extractMemoryIDs(t, resp)
	if len(ids) < 3 {
		t.Fatalf("expected at least 3 memories, got %d", len(ids))
	}

	// Every memory mentioning the dog must carry the same normalized name, or
	// they are attached to different nodes.
	byMemory, err := repo.GetMemoryEntities(ctx, ids)
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	mentioning := 0
	for _, names := range byMemory {
		for _, n := range names {
			if n == "biscuit" {
				mentioning++
			}
		}
	}
	if mentioning < 3 {
		t.Fatalf("only %d of 3 memories resolved to the entity `biscuit`; "+
			"`Biscuit` and `Biscuit's` must reach one node", mentioning)
	}

	// The decisive check: one lookup by the entity returns all three, which is
	// only possible if they share a node.
	found, err := repo.FindMemoriesByEntities(ctx, projectID, []string{"Biscuit"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	if len(found) < 3 {
		t.Errorf("a lookup for `Biscuit` returned %d memories, want 3: the "+
			"variant spellings created separate nodes", len(found))
	}
}

// TestExtract_TwoMemoriesSharingAnEntityAreOneHopApart is the third acceptance
// criterion, and the one the multi-hop failures were actually about.
//
// The two memories below share no meaningful keyword and are not paraphrases,
// so neither retriever connects them. They are about the same dog, and that is
// the connection the graph is supposed to carry.
func TestExtract_TwoMemoriesSharingAnEntityAreOneHopApart(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-hop-%d", time.Now().UnixNano())

	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she adopted a rescue dog named Biscuit.\n" +
			"Caroline said that Biscuit is terrified of thunderstorms.\n" +
			"Melanie said that the quarterly tax filing deadline is in April.",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(resp.Memories) < 3 {
		t.Fatalf("expected at least 3 memories, got %d", len(resp.Memories))
	}

	var adoptionID, thunderID, taxID uuid.UUID
	for _, m := range resp.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		switch {
		case strings.Contains(m.Content, "adopted"):
			adoptionID = id
		case strings.Contains(m.Content, "thunderstorms"):
			thunderID = id
		case strings.Contains(m.Content, "tax filing"):
			taxID = id
		}
	}
	if adoptionID == uuid.Nil || thunderID == uuid.Nil {
		t.Fatal("the two Biscuit memories were not both extracted")
	}

	// One hop: from the adoption memory, through the entity it names, to the
	// thunderstorm memory.
	names, err := repo.GetMemoryEntities(ctx, []uuid.UUID{adoptionID})
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	reached, err := repo.FindMemoriesByEntities(ctx, projectID, names[adoptionID], 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}

	foundThunder := false
	foundTax := false
	for _, m := range reached {
		switch m.ID {
		case thunderID:
			foundThunder = true
		case taxID:
			foundTax = true
		}
	}
	if !foundThunder {
		t.Error("the thunderstorm memory was not reachable in one hop from the " +
			"adoption memory; both are about Biscuit, and that is the connection " +
			"multi-hop questions need")
	}
	// The other direction. Reaching everything is as useless as reaching
	// nothing: a dense graph carries no information and makes every traversal
	// expensive, which is the failure a2f53ac hit with semantic linking.
	if foundTax {
		t.Error("the unrelated tax memory was reachable through Biscuit's entities; " +
			"linking everything to everything carries no information")
	}
}

// TestQuery_EntityMatchOutranksAPurelySemanticMatch is the fourth acceptance
// criterion: a memory whose entities match the query must rank above one that
// only matches semantically.
//
// Two details make this measure the entity signal rather than the keyword
// retriever:
//
//   - The query uses the possessive, "Biscuit's". extractKeywords keeps
//     intra-word punctuation, so the keyword is "biscuit's", and CONTAINS
//     cannot find that in "Biscuit is". Neither memory matches it lexically,
//     which is what puts them on equal footing.
//   - The rival is worded near-identically and stored *after* the target, so
//     recency favours the wrong answer. Anything that ordered these correctly
//     without the entity signal would have to be doing so by accident.
func TestQuery_EntityMatchOutranksAPurelySemanticMatch(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-rank-%d", time.Now().UnixNano())

	// Stored first, so recency works against it.
	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Caroline said that Biscuit is terrified of loud storms at night.",
	}); err != nil {
		t.Fatalf("extract target: %v", err)
	}
	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Melanie said that her cat is terrified of loud storms at night.",
	}); err != nil {
		t.Fatalf("extract rival: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "What is Biscuit's greatest fear?",
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the query returned nothing")
	}

	if !strings.Contains(resp.Results[0].Memory.Content, "Biscuit") {
		t.Errorf("the single top result is %q; with neither memory matching a "+
			"keyword and both worded alike, being about the query's subject is "+
			"the only signal that separates them",
			resp.Results[0].Memory.Content)
	}
}

// The other direction, and the one that can do damage.
//
// In a corpus about Caroline, nearly every memory names Caroline. If naming
// the subject outranked answering the question, every query would return the
// same memories in the same order regardless of what was asked. Mem0's
// ENTITY_BOOST_WEIGHT of 0.5 would do exactly that here, which is why this
// engine's boost is an order of magnitude smaller.
func TestQuery_EntityBoostCannotOverturnARealRelevanceDifference(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-rank-neg-%d", time.Now().UnixNano())

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she adopted a rescue dog last spring.\n" +
			"Caroline said that she walks along the river each morning.\n" +
			"Caroline said that she works as a nurse at the county hospital.\n" +
			"Caroline said that she repainted the kitchen a pale green colour.\n" +
			"The quarterly corporate tax filing deadline falls in April each year.",
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// The question is about the tax deadline, which is the one memory not
	// naming Caroline. Four memories do name her, so a boost able to overturn
	// relevance would put all four first.
	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "When does Caroline need to file the quarterly corporate tax filing deadline?",
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the query returned nothing")
	}

	if !strings.Contains(resp.Results[0].Memory.Content, "tax filing") {
		t.Errorf("the single top result is %q; the memory that answers the question "+
			"must outrank four that merely name the same person",
			resp.Results[0].Memory.Content)
	}
}

// TestQuery_EntityRetrievalReachesAMemoryNeitherRetrieverWould is the recall
// half of the feature, as distinct from the ordering half above.
//
// The decoys matter. With only a handful of memories every query returns
// everything and the test proves nothing; the failure this pins is a memory
// that loses its top-K slot to memories which merely share a common word.
//
// Measured before entity retrieval fed the merge: this exact query returned
// three memories about pet noise studies and not the one about Biscuit. The
// query's own possessive is why -- extractKeywords keeps intra-word
// punctuation, so the keyword is "biscuit's", and CONTAINS cannot find that in
// "Biscuit bolts". The entity node can, because identity is decided on the
// normalized name.
func TestQuery_EntityRetrievalReachesAMemoryNeitherRetrieverWould(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-recall-%d", time.Now().UnixNano())

	const target = "Caroline said that Biscuit bolts under the bed whenever it thunders."
	lines := []string{target}
	// Decoys sharing the query's ordinary words ("fear") but naming nothing.
	// Enough of them to fill top-K several times over.
	for i := 0; i < 15; i++ {
		lines = append(lines, fmt.Sprintf(
			"Melanie said that the fear of loud noises affects many pets in study %d.", i+100))
	}

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: strings.Join(lines, "\n"),
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "What is Biscuit's greatest fear?",
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	found := false
	for _, r := range resp.Results {
		if strings.Contains(r.Memory.Content, "bolts under the bed") {
			found = true
			break
		}
	}
	if !found {
		var got []string
		for _, r := range resp.Results {
			got = append(got, r.Memory.Content)
		}
		t.Errorf("a query naming Biscuit did not reach the memory about him, "+
			"losing its slot to memories that merely share a common word. Got: %v", got)
	}
}

// Entities are scoped to the project because the project is the tenant
// boundary everywhere else in this engine. A shared entity node would make one
// tenant's memories reachable from another's in a single hop, which is a
// harder failure to notice than a wrong ranking.
func TestQuery_EntitiesDoNotCrossProjects(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	stamp := time.Now().UnixNano()
	projectA := fmt.Sprintf("entity-tenant-a-%d", stamp)
	projectB := fmt.Sprintf("entity-tenant-b-%d", stamp)

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectA,
		Conversation: "Caroline said that Biscuit is terrified of thunderstorms.",
	}); err != nil {
		t.Fatalf("extract into project A: %v", err)
	}
	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectB,
		Conversation: "Caroline said that Biscuit won the county dog show.",
	}); err != nil {
		t.Fatalf("extract into project B: %v", err)
	}

	found, err := repo.FindMemoriesByEntities(ctx, projectA, []string{"Biscuit"}, 50)
	if err != nil {
		t.Fatalf("find by entity: %v", err)
	}
	for _, m := range found {
		if m.ProjectID != projectA {
			t.Errorf("an entity lookup in project %s returned a memory from %s: "+
				"entities must respect the tenant boundary", projectA, m.ProjectID)
		}
		if strings.Contains(m.Content, "dog show") {
			t.Error("project B's memory was reachable through project A's entity node")
		}
	}
	if len(found) == 0 {
		t.Error("the scoping is refusing everything, including the project's own memories")
	}
}

// A query naming no entity must behave exactly as it did before entities
// existed. Most queries name nothing, so a regression here would be a
// regression in the common case.
func TestQuery_WithoutEntitiesIsUnaffected(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("entity-none-%d", time.Now().UnixNano())

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that the deployment pipeline runs on Kubernetes.\n" +
			"Caroline said that the database migration finished successfully.",
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "what does the deployment pipeline run on",
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("a query naming no entity returned nothing")
	}
	if !strings.Contains(resp.Results[0].Memory.Content, "Kubernetes") {
		t.Errorf("the top result is %q; a query with no entities must rank exactly "+
			"as it did before entities existed", resp.Results[0].Memory.Content)
	}
}
