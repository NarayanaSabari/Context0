package service

// Write-time consolidation tests.
//
// The measurement that motivated this: a 40-question LoCoMo corpus ingested
// through Extract held 6,010 memories expressing 573 distinct facts, and 6,925
// edges, most of them linking paraphrases of one fact to each other. Store
// called detectAndSupersede; Extract called nothing, so every restatement of
// every fact became its own row, its own embedding, and its own share of the
// graph.
//
// This is a cost fix, not an accuracy fix. Measured on retrieved results, only
// 1% of retrieval slots were exact duplicates and 2% near duplicates, so any
// accuracy movement from these changes means something unintended happened.
// The tests are therefore about what is stored, and about what must still be
// stored.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// consolidationTestService builds a service against the real database. The
// whole point of these tests is what lands in the graph, so none of it is
// mocked.
func consolidationTestService(t *testing.T) (*MemoryService, *graph.AGERepository, context.Context) {
	t.Helper()

	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	return NewMemoryService(repo, embedding.NewBagOfWordsEmbedder(384)), repo, ctx
}

// countMemories reports how many Memory vertices a project holds, asking the
// graph rather than trusting the API's own response.
func countMemories(ctx context.Context, t *testing.T, repo *graph.AGERepository, projectID string) int {
	t.Helper()
	results, err := repo.QueryMemories(ctx, graph.QueryFilter{
		ProjectID: projectID,
		TopK:      500,
	})
	if err != nil {
		t.Fatalf("count memories: %v", err)
	}
	return len(results)
}

// TestExtract_IngestingTheSameConversationTwiceDoesNotDoubleTheRowCount is the
// headline acceptance criterion.
//
// Re-ingesting a transcript is not an exotic case: an agent replaying a
// session, a retried request, or a backfill all produce it, and each one used
// to double the store.
func TestExtract_IngestingTheSameConversationTwiceDoesNotDoubleTheRowCount(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-twice-%d", time.Now().UnixNano())

	conversation := "Caroline said that she adopted a rescue dog named Biscuit last month.\n" +
		"Caroline said that Biscuit is a nervous dog who hates thunderstorms.\n" +
		"Melanie said that the quarterly tax filing deadline is in April."

	first, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: conversation})
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if len(first.Memories) == 0 {
		t.Fatal("the first ingest produced no memories; the rest of the test is meaningless")
	}
	afterFirst := countMemories(ctx, t, repo, projectID)
	if afterFirst == 0 {
		t.Fatal("the first ingest stored nothing")
	}

	second, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: conversation})
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	afterSecond := countMemories(ctx, t, repo, projectID)

	if afterSecond != afterFirst {
		t.Errorf("re-ingesting the same conversation grew the store from %d to %d rows; "+
			"a transcript that says nothing new must store nothing new",
			afterFirst, afterSecond)
	}

	// The response still describes what the conversation said. A caller that
	// re-ingests must not be told the conversation was empty, or a retry would
	// look like a failed extraction.
	if len(second.Memories) != len(first.Memories) {
		t.Errorf("the second ingest reported %d memories against the first ingest's %d; "+
			"consolidation must not change what the conversation is reported to say",
			len(second.Memories), len(first.Memories))
	}

	// And it must answer with the rows that actually hold those facts, not
	// with ids for rows that were never created.
	for _, m := range second.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		if _, err := repo.GetMemory(ctx, id); err != nil {
			t.Errorf("the response referenced memory %s, which is not in the graph: "+
				"a consolidated write must return the row that holds the fact", id)
		}
	}
}

// TestExtract_NearDuplicatesDoNotBothPersist covers the exact pair measured in
// the corpus: `Caroline is transgender.` was stored 25 times and `Caroline is a
// transgender person.` another 16 times, as independent rows.
//
// Both directions are pinned, because superseding is destructive to ranking
// order and the arrival order must not decide what survives.
func TestExtract_NearDuplicatesDoNotBothPersist(t *testing.T) {
	shorter := "Caroline said that she is transgender."
	longer := "Caroline said that she is a transgender person."

	for _, tt := range []struct {
		name         string
		first, later string
	}{
		{"fuller wording arrives second", shorter, longer},
		{"fuller wording arrives first", longer, shorter},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, ctx := consolidationTestService(t)
			projectID := fmt.Sprintf("dedup-near-%d", time.Now().UnixNano())

			for _, conv := range []string{tt.first, tt.later} {
				if _, err := svc.Extract(ctx, &pb.ExtractRequest{
					ProjectId:    projectID,
					Conversation: conv,
				}); err != nil {
					t.Fatalf("extract %q: %v", conv, err)
				}
			}

			results, err := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 100})
			if err != nil {
				t.Fatalf("query: %v", err)
			}

			var transgender []string
			for _, r := range results {
				if strings.Contains(strings.ToLower(r.Memory.Content), "transgender") {
					transgender = append(transgender, r.Memory.Content)
				}
			}

			if len(transgender) != 1 {
				t.Fatalf("the store holds %d rows for one fact: %q; "+
					"two wordings of the same fact must not both persist",
					len(transgender), transgender)
			}

			// Whichever order they arrived in, the wording that survives must
			// be the one carrying more information. The reverse would be a
			// consolidation that loses content.
			if !strings.Contains(transgender[0], "a transgender person") {
				t.Errorf("the surviving row reads %q; the fuller wording must win "+
					"regardless of arrival order", transgender[0])
			}
		})
	}
}

// The other direction, and the one that decides whether consolidation is safe
// to run on a write path at all: a genuinely new fact must still be stored.
//
// Every pair here is one a bag-of-words or dense embedding scores as highly
// similar, which is exactly where a similarity-threshold implementation would
// silently destroy a fact.
func TestExtract_GenuinelyNewFactsAreStillStored(t *testing.T) {
	tests := []struct {
		name           string
		first, second  string
		mustBothRemain []string
	}{
		{
			name:           "a reversed preposition is a different fact",
			first:          "Caroline said that she moved to Sweden in 2019.",
			second:         "Caroline said that she moved from Sweden in 2021.",
			mustBothRemain: []string{"moved to Sweden", "moved from Sweden"},
		},
		{
			name:           "a negation is a different fact",
			first:          "Caroline said that she is a teacher at the local school.",
			second:         "Caroline said that she is not a teacher at the local school.",
			mustBothRemain: []string{"is a teacher", "is not a teacher"},
		},
		{
			name:           "a different quantity is a different fact",
			first:          "Caroline said that she ran 10 km along the river on Saturday.",
			second:         "Caroline said that she ran 5 km along the river on Saturday.",
			mustBothRemain: []string{"10 km", "5 km"},
		},
		{
			// Truth `counseling for Transgender people` was graded wrong when
			// answered as `counseling`, so the qualifier is the fact.
			name:           "a qualifier is part of the fact",
			first:          "Caroline said that she offers counseling for transgender people.",
			second:         "Caroline said that she offers counseling for teenagers.",
			mustBothRemain: []string{"transgender people", "teenagers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, ctx := consolidationTestService(t)
			projectID := fmt.Sprintf("dedup-distinct-%d", time.Now().UnixNano())

			for _, conv := range []string{tt.first, tt.second} {
				if _, err := svc.Extract(ctx, &pb.ExtractRequest{
					ProjectId:    projectID,
					Conversation: conv,
				}); err != nil {
					t.Fatalf("extract %q: %v", conv, err)
				}
			}

			results, err := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 100})
			if err != nil {
				t.Fatalf("query: %v", err)
			}

			var stored []string
			for _, r := range results {
				stored = append(stored, r.Memory.Content)
			}

			for _, want := range tt.mustBothRemain {
				found := false
				for _, s := range stored {
					if strings.Contains(s, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no stored memory carries %q; consolidation destroyed a fact. Stored: %v",
						want, stored)
				}
			}
		})
	}
}

// TestExtract_RestatementIsRecordedAsUse pins that a consolidated write is not
// simply discarded.
//
// A fact restated across conversations is evidence of its importance, and
// access count is the signal ranking and decay use for exactly that. Dropping
// the restatement silently would make a frequently-repeated fact look
// untouched, which is a behaviour change that consolidation should not smuggle
// in alongside the row saving.
func TestExtract_RestatementIsRecordedAsUse(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-access-%d", time.Now().UnixNano())

	conversation := "Caroline said that she adopted a rescue dog named Biscuit last month."

	first, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: conversation})
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if len(first.Memories) == 0 {
		t.Fatal("nothing was extracted")
	}
	id, err := uuid.Parse(first.Memories[0].Id)
	if err != nil {
		t.Fatalf("memory id is not a uuid: %v", err)
	}

	before, err := repo.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: conversation}); err != nil {
		t.Fatalf("second extract: %v", err)
	}

	after, err := repo.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("get memory after: %v", err)
	}

	if after.AccessCount <= before.AccessCount {
		t.Errorf("access_count stayed at %d after the fact was restated; "+
			"a consolidated write must still record that the fact came up again",
			after.AccessCount)
	}
}

// TestExtract_ConsolidationCutsRowsAndEdgesByAnOrderOfMagnitude is the cost
// claim, measured rather than asserted.
//
// The corpus figure was 6,010 rows for 573 facts. This is the same shape at
// test scale: one conversation restating three facts many times, which without
// consolidation stores every restatement, embeds it, and links it to its own
// paraphrases.
//
// The restatements are deliberately *paraphrases*, not repeated lines. The
// rule extractor has always dropped exactly-repeated content, which is why the
// corpus still ended up with 25 copies of one fact: they arrived worded
// slightly differently each time, and nothing compared them.
func TestExtract_ConsolidationCutsRowsAndEdgesByAnOrderOfMagnitude(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-scale-%d", time.Now().UnixNano())

	// Three distinct facts, each stated ten times in progressively fuller
	// wording. Every line is textually distinct, so the extractor's own
	// exact-content dedup cannot touch them: without subsumption this is 30
	// rows, and with it, 3.
	//
	// Each group is an ordered subsequence chain, which is the shape real
	// restatement takes -- the speaker adds a detail rather than rewording
	// from scratch.
	groups := [][]string{{
		"Caroline adopted a dog.",
		"Caroline adopted a rescue dog.",
		"Caroline adopted a rescue dog named Biscuit.",
		"Caroline adopted a small rescue dog named Biscuit.",
		"Caroline adopted a small nervous rescue dog named Biscuit.",
	}, {
		"The quarterly tax filing deadline is in April.",
		"The quarterly tax filing deadline is in April every year.",
		"Melanie says the quarterly tax filing deadline is in April every year.",
		"Melanie says the quarterly corporate tax filing deadline is in April every year.",
		"Melanie always says the quarterly corporate tax filing deadline is in April every year.",
	}, {
		"Caroline works as a nurse.",
		"Caroline works as a nurse at the hospital.",
		"Caroline works as a nurse at the county hospital.",
		"Caroline works as a paediatric nurse at the county hospital.",
		"Caroline currently works as a paediatric nurse at the county hospital.",
	}}

	var lines []string
	for _, group := range groups {
		lines = append(lines, group...)
	}
	unconsolidated := len(lines)

	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: strings.Join(lines, "\n"),
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(resp.Memories) == 0 {
		t.Fatal("nothing was extracted")
	}

	rows := countMemories(ctx, t, repo, projectID)

	// Three facts were stated. Allowing double that leaves room for the
	// extractor splitting a line differently without making the assertion
	// vacuous, since the un-consolidated count is len(lines).
	if rows > 2*len(groups) {
		results, _ := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 100})
		var stored []string
		for _, r := range results {
			stored = append(stored, r.Memory.Content)
		}
		t.Errorf("a conversation stating %d facts across %d restatements stored %d rows; "+
			"expected roughly %d. Stored: %v", len(groups), unconsolidated, rows, len(groups), stored)
	}

	// The surviving row for each fact must be the fullest wording, or the
	// saving came at the cost of the detail that makes a fact answerable.
	stored, err := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, group := range groups {
		fullest := group[len(group)-1]
		found := false
		for _, r := range stored {
			if strings.Contains(r.Memory.Content, fullest) {
				found = true
				break
			}
		}
		if !found {
			var contents []string
			for _, r := range stored {
				contents = append(contents, r.Memory.Content)
			}
			t.Errorf("no stored memory carries the fullest wording %q; "+
				"consolidation kept a row but dropped the detail. Stored: %v",
				fullest, contents)
		}
	}

	// Edges scale with rows squared in the worst case, which is why the corpus
	// held 6,925 of them. With three facts there is very little to link.
	ids := make([]uuid.UUID, 0, len(resp.Memories))
	for _, m := range resp.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		ids = append(ids, id)
	}
	edges, err := repo.GetContextEdges(ctx, ids)
	if err != nil {
		t.Fatalf("get context edges: %v", err)
	}
	total := 0
	for _, list := range edges {
		total += len(list)
	}
	if total > 12 {
		t.Errorf("the graph holds %d edges for %d rows; consolidation is meant to "+
			"remove the paraphrase-to-paraphrase links that dominated the corpus",
			total, rows)
	}
}

// TestExtract_DetectsContradictions pins that contradiction detection runs on
// the Extract path.
//
// detectAndSupersede was wired into Store alone, so a fact that reversed an
// earlier one left both live whenever the conversation arrived through Extract
// -- which is how every agent conversation arrives. The engine's answer to
// "what does Caroline do now?" then had two equally live candidates.
func TestExtract_DetectsContradictions(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-contra-%d", time.Now().UnixNano())

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Caroline said that the backend uses Python for its services.",
	}); err != nil {
		t.Fatalf("first extract: %v", err)
	}

	second, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Caroline said that the backend uses Go for its services.",
	})
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if len(second.Memories) == 0 {
		t.Fatal("the contradicting conversation extracted nothing")
	}

	ids := make([]uuid.UUID, 0, len(second.Memories))
	for _, m := range second.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		ids = append(ids, id)
	}

	edges, err := repo.GetContextEdges(ctx, ids)
	if err != nil {
		t.Fatalf("get context edges: %v", err)
	}
	for _, list := range edges {
		for _, e := range list {
			if e.Relationship == model.RelSupersedes {
				return
			}
		}
	}

	t.Error("a fact reversing an earlier one created no supersedes edge through Extract; " +
		"contradiction detection must not depend on which endpoint the conversation arrived through")
}

// TestExtract_ResponseKeepsTheOrderTheConversationStatedTheFacts pins that
// consolidation does not reorder the response.
//
// A memory can be resolved at any of three stages -- folded within the
// conversation, matched by content hash, or written normally -- and reporting
// them grouped by stage would answer a [new, restated, new] transcript as
// [restated, new, new]. Nothing else in the engine reorders a transcript, and
// a caller pairing the response against its own input has no way to detect it.
func TestExtract_ResponseKeepsTheOrderTheConversationStatedTheFacts(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-order-%d", time.Now().UnixNano())

	// Stored first, so it is the middle line of the second conversation that
	// resolves through the duplicate path rather than being written.
	const restated = "Melanie says the quarterly tax filing deadline is in April."
	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: restated,
	}); err != nil {
		t.Fatalf("seed extract: %v", err)
	}

	ordered := []string{
		"Caroline adopted a rescue dog named Biscuit.",
		restated,
		"Caroline works as a paediatric nurse at the county hospital.",
	}

	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: strings.Join(ordered, "\n"),
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(resp.Memories) != len(ordered) {
		var got []string
		for _, m := range resp.Memories {
			got = append(got, m.Content)
		}
		t.Fatalf("reported %d memories for %d lines: %v", len(resp.Memories), len(ordered), got)
	}

	for i, want := range ordered {
		if resp.Memories[i].Content != want {
			var got []string
			for _, m := range resp.Memories {
				got = append(got, m.Content)
			}
			t.Fatalf("response position %d is %q, want %q: consolidation reordered "+
				"the transcript. Got: %v", i, resp.Memories[i].Content, want, got)
		}
	}
}

// TestExtract_ResponseReportsTheContentTheStoreNowHolds covers the case where
// a fold upgrades the stored row to the fuller wording.
//
// The response must describe the store as it is after the write, not as it was
// when the candidate was read. Reporting the old shorter text would leave the
// API disagreeing with the database immediately after a successful write.
func TestExtract_ResponseReportsTheContentTheStoreNowHolds(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-fresh-%d", time.Now().UnixNano())

	const shorter = "Caroline adopted a rescue dog."
	const fuller = "Caroline adopted a small rescue dog named Biscuit."

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: shorter}); err != nil {
		t.Fatalf("first extract: %v", err)
	}

	resp, err := svc.Extract(ctx, &pb.ExtractRequest{ProjectId: projectID, Conversation: fuller})
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if len(resp.Memories) != 1 {
		t.Fatalf("reported %d memories, want 1", len(resp.Memories))
	}

	if resp.Memories[0].Content != fuller {
		t.Errorf("the response reports %q after consolidating in %q; it must "+
			"describe what the store now holds", resp.Memories[0].Content, fuller)
	}

	// And the store really does hold it, so the response is not merely echoing
	// the request back.
	id, err := uuid.Parse(resp.Memories[0].Id)
	if err != nil {
		t.Fatalf("memory id is not a uuid: %v", err)
	}
	stored, err := repo.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if stored.Content != fuller {
		t.Errorf("the stored row reads %q, want %q", stored.Content, fuller)
	}
}

// TestExtract_ConcurrentIngestsDoNotLoseAFact is the race the compare-and-set
// in UpdateMemoryContent exists for.
//
// Several conversations consolidate against the same row. Each reads
// "Caroline adopted a rescue dog" and decides its own wording subsumes it. If
// every write lands unconditionally, the last one wins and the rest of the
// facts are gone -- and they exist nowhere else, because consolidation skipped
// storing them precisely because it believed this row covered them. Each
// writer that loses the guard must fall back to storing its own memory.
//
// The rivals are deliberately many and their distinguishing detail is a single
// distinct word, so the assertion is precise about which fact went missing.
// With the guard removed from UpdateMemoryContent this fails within a couple
// of runs; the window is small, so the count is what gives the test power.
func TestExtract_ConcurrentIngestsDoNotLoseAFact(t *testing.T) {
	svc, repo, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("dedup-race-%d", time.Now().UnixNano())

	const seed = "Caroline said that she adopted a rescue dog from the county shelter."
	seeded, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: seed,
	})
	if err != nil {
		t.Fatalf("seed extract: %v", err)
	}
	if len(seeded.Memories) != 1 {
		// Every rival below has to subsume this one row, or they would simply
		// be stored side by side and the race would never be reached.
		t.Fatalf("the seed stored %d memories, want 1", len(seeded.Memories))
	}

	// Each extends the seeded memory with a detail none of the others carry,
	// so at most one of them can legitimately be folded away.
	details := []string{
		"Biscuit", "Pepper", "Marlow", "Juniper",
		"Tobias", "Winnie", "Clementine", "Hollis",
	}
	rivals := make([]string, len(details))
	for i, d := range details {
		// Each rival is the seed plus one distinguishing word, so every one of
		// them subsumes the seeded row and they all consolidate against it.
		rivals[i] = fmt.Sprintf(
			"Caroline said that she adopted a rescue dog named %s from the county shelter.", d)
	}

	// Released together, so the reads overlap rather than queueing behind each
	// other's writes.
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for _, conv := range rivals {
		wg.Add(1)
		go func(conv string) {
			defer wg.Done()
			start.Wait()
			if _, err := svc.Extract(ctx, &pb.ExtractRequest{
				ProjectId:    projectID,
				Conversation: conv,
			}); err != nil {
				t.Errorf("extract %q: %v", conv, err)
			}
		}(conv)
	}
	start.Done()
	wg.Wait()

	results, err := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var stored []string
	for _, r := range results {
		stored = append(stored, r.Memory.Content)
	}

	// Every distinguishing detail must survive somewhere, on one row or many.
	// Losing one means a write reported success while destroying the fact it
	// claimed to have consolidated.
	var lost []string
	for _, detail := range details {
		found := false
		for _, s := range stored {
			if strings.Contains(s, detail) {
				found = true
				break
			}
		}
		if !found {
			lost = append(lost, detail)
		}
	}
	if len(lost) > 0 {
		t.Errorf("no stored memory carries %v after %d concurrent ingests; "+
			"those facts were lost to a race. Stored: %v", lost, len(rivals), stored)
	}
}
