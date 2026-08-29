package graph

// Integration tests for full-text keyword retrieval. Real Postgres, because
// the entire point of this change is which query plan runs and what the text
// search configuration does to a word, neither of which a mock can tell you.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSearchByText_DoesNotMatchInsideWords is the headline acceptance
// criterion.
//
// Cypher CONTAINS is substring matching, so `go` matched `going`, `mango` and
// `algorithm` alike. to_tsvector lexes into words first, so a query for `go`
// reaches `going` -- same word, stemmed -- and nothing else.
func TestSearchByText_DoesNotMatchInsideWords(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	shouldMatch := storeMemory(t, repo, ctx, newMemory(projectID, "we are going to production on Friday"))
	mango := storeMemory(t, repo, ctx, newMemory(projectID, "she ate a mango for breakfast"))
	algorithm := storeMemory(t, repo, ctx, newMemory(projectID, "the algorithm sorts in linear time"))

	got, err := repo.SearchByText(ctx, projectID, []string{"go"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	matched := make(map[uuid.UUID]bool, len(got))
	for _, r := range got {
		matched[r.Memory.ID] = true
	}

	if !matched[shouldMatch.ID] {
		t.Error("a query for `go` did not reach `going`; stemming is what makes " +
			"full-text search better than substring matching, not just different")
	}
	if matched[mango.ID] {
		t.Error("a query for `go` matched `mango`; substring matching inside words " +
			"is the behaviour this replaces")
	}
	if matched[algorithm.ID] {
		t.Error("a query for `go` matched `algorithm`")
	}
}

// Rare terms must outweigh common ones, which `CONTAINS` cannot express at
// all: to it, `the` and `zqxjklmw` are the same kind of evidence.
func TestSearchByText_RareTermsOutrankCommonOnes(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	rare := storeMemory(t, repo, ctx, newMemory(projectID, "the distinctive zqxjklmw marker lives here"))
	for i := 0; i < 20; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("the deployment %d ran the usual way through the pipeline", i)))
	}

	got, err := repo.SearchByText(ctx, projectID, []string{"zqxjklmw", "the"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the search returned nothing")
	}
	if got[0].Memory.ID != rare.ID {
		t.Errorf("the top hit is %q; a memory containing the rare term must outrank "+
			"twenty containing only the common one", got[0].Memory.Content)
	}

	// And the rank must actually differ, rather than the order being luck.
	if got[0].Score <= 0 {
		t.Errorf("the top hit scored %v; ts_rank_cd must grade the match", got[0].Score)
	}
	if len(got) > 1 && got[0].Score <= got[len(got)-1].Score {
		t.Errorf("the best and worst hits scored %v and %v; the ranking carries no "+
			"information", got[0].Score, got[len(got)-1].Score)
	}
}

// A memory matching more of the query's terms must rank above one matching
// fewer, which is what makes the raw score worth normalising rather than
// thresholding.
func TestSearchByText_RanksByHowMuchOfTheQueryIsMatched(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	all := storeMemory(t, repo, ctx, newMemory(projectID,
		"Caroline adopted a nervous rescue dog named Biscuit"))
	storeMemory(t, repo, ctx, newMemory(projectID, "Caroline went to the shops"))
	storeMemory(t, repo, ctx, newMemory(projectID, "the dog barked"))

	got, err := repo.SearchByText(ctx, projectID,
		[]string{"caroline", "rescue", "dog", "biscuit"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the search returned nothing")
	}
	if got[0].Memory.ID != all.ID {
		t.Errorf("the top hit is %q; the memory matching every query term must "+
			"rank first", got[0].Memory.Content)
	}
}

// The project is the tenant boundary everywhere else in this engine, and the
// keyword retriever moving from Cypher to SQL is exactly where it could be
// dropped without anything noticing.
func TestSearchByText_IsScopedToTheProject(t *testing.T) {
	repo, ctx := testRepo(t)
	projectA, projectB := newProjectID(t), newProjectID(t)

	inA := storeMemory(t, repo, ctx, newMemory(projectA, "the zqxjklmw marker in project A"))
	storeMemory(t, repo, ctx, newMemory(projectB, "the zqxjklmw marker in project B"))

	got, err := repo.SearchByText(ctx, projectA, []string{"zqxjklmw"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Memory.ID != inA.ID {
		t.Errorf("a scoped search returned %d results; it must not cross projects", len(got))
	}

	// The unscoped search is reachable through the API by omitting project_id,
	// and must still see both.
	unscoped, err := repo.SearchByText(ctx, "", []string{"zqxjklmw"}, 10)
	if err != nil {
		t.Fatalf("unscoped search: %v", err)
	}
	if len(unscoped) < 2 {
		t.Errorf("an unscoped search returned %d results, want at least 2", len(unscoped))
	}
}

// Terms are OR-ed, not AND-ed. This is the recall-oriented retriever in a
// hybrid, and requiring every term turns a five-word question into an empty
// result set.
func TestSearchByText_OrsItsTerms(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	storeMemory(t, repo, ctx, newMemory(projectID, "Caroline walks by the river each morning"))

	got, err := repo.SearchByText(ctx, projectID,
		[]string{"caroline", "helicopter", "submarine"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("a query where one of three terms matches returned %d results; "+
			"AND-ing the terms empties the result set for any real question", len(got))
	}
}

// A query of nothing but stop words has nothing to match. Postgres says so
// with a notice rather than an error, and the caller must see an empty result
// rather than a failure.
func TestSearchByText_HandlesQueriesWithNothingToMatch(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	storeMemory(t, repo, ctx, newMemory(projectID, "an ordinary memory about deployment"))

	for _, tc := range []struct {
		name     string
		keywords []string
		limit    int
	}{
		{"stop words only", []string{"the", "and", "of"}, 10},
		{"no keywords", nil, 10},
		{"zero limit", []string{"deployment"}, 0},
		{"negative limit", []string{"deployment"}, -1},
	} {
		got, err := repo.SearchByText(ctx, projectID, tc.keywords, tc.limit)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: returned %d results, want none", tc.name, len(got))
		}
	}
}

// Query terms come from user text, so they reach websearch_to_tsquery as
// arbitrary strings. They are bound as parameters, and websearch_to_tsquery is
// specifically the parser that does not error on punctuation, which together
// mean a hostile query is a query that finds nothing rather than one that
// fails or does something else.
func TestSearchByText_TreatsHostileQueriesAsText(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "an ordinary memory"))

	hostile := [][]string{
		{"'; DROP TABLE \"Memory\"; --"},
		{"' OR '1'='1"},
		{"deployment & | ! ( ) : * <->"},
		{strings.Repeat("a", 500)},
		{"unbalanced \" quote"},
	}
	for _, kws := range hostile {
		if _, err := repo.SearchByText(ctx, projectID, kws, 10); err != nil {
			t.Errorf("a hostile query %q errored rather than simply not matching: %v", kws, err)
		}
	}

	// Nothing was dropped or altered.
	if _, err := repo.GetMemory(ctx, mem.ID); err != nil {
		t.Errorf("the memory is gone after a hostile query: %v", err)
	}
}

// The limit runs in SQL, where the index is, so only the winners cross the
// wire. An unbounded keyword search on a large corpus is the sequential scan
// this change exists to remove, arriving from the other direction.
func TestSearchByText_RespectsTheLimit(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i := 0; i < 15; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("deployment number %d finished cleanly", i)))
	}

	const limit = 5
	got, err := repo.SearchByText(ctx, projectID, []string{"deployment"}, limit)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != limit {
		t.Errorf("a search with limit %d returned %d results", limit, len(got))
	}
}

// Results arrive in rank order and must stay that way through hydration, which
// fetches them from the graph in whatever order the graph returns.
func TestSearchByText_HydrationPreservesRankOrder(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// Stored oldest-first so that any accidental fallback to created_at
	// ordering produces a visibly different result.
	best := newMemory(projectID, "zqxjklmw zqxjklmw zqxjklmw the marker appears repeatedly")
	best.CreatedAt = time.Now().UTC().Add(-time.Hour)
	storeMemory(t, repo, ctx, best)

	for i := 0; i < 5; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("the zqxjklmw marker appears once in filler %d", i)))
	}

	got, err := repo.SearchByText(ctx, projectID, []string{"zqxjklmw"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected several hits, got %d", len(got))
	}
	if got[0].Memory.ID != best.ID {
		t.Errorf("the top hit is %q; hydration must preserve the order ts_rank_cd "+
			"computed, not the order the graph returned the rows in",
			got[0].Memory.Content)
	}

	// Scores must arrive descending, or the caller's normalisation is applied
	// to an order that means nothing.
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("result %d scored %v after %v; the ranking order was lost",
				i, got[i].Score, got[i-1].Score)
		}
	}
}

// The GIN index is what makes this better than CONTAINS rather than merely
// different: CONTAINS could not use an index at all, even under
// enable_seqscan=off, because no operator class binds
// agtype_string_match_contains to one.
//
// The index expression must match the query expression exactly, including the
// text search configuration. A mismatch produces an index the planner silently
// ignores, which looks identical to it working.
func TestSearchByText_UsesTheFullTextIndex(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i := 0; i < 40; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("deployment number %d about kubernetes rollouts", i)))
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// enable_seqscan=off forces the planner to prefer any usable index at
	// almost any cost. A sequential scan under it means no index can serve the
	// predicate, which is the signature the CONTAINS investigation identified.
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	explain := fmt.Sprintf(
		`EXPLAIN (COSTS OFF) SELECT %s FROM %s."Memory" `+
			`WHERE to_tsvector('%s', %s) @@ websearch_to_tsquery('%s', $1) LIMIT 10`,
		idExpr, GraphName, textSearchConfig, contentExpr, textSearchConfig,
	)
	rows, err := tx.Query(ctx, explain, "kubernetes")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("explain: %v", err)
	}

	if !strings.Contains(plan.String(), "memory_content_fts_idx") {
		t.Errorf("the keyword query does not use the full-text index:\n%s\n"+
			"An index the planner cannot use is indistinguishable from a working "+
			"one until the corpus grows", plan.String())
	}
}

// A query term containing full-text search syntax must be a term, not an
// operator.
//
// Joining terms into one "a OR b OR c" string and handing that to
// websearch_to_tsquery lets a term rewrite the query around it. Verified
// against Postgres 18:
//
//	'cats OR say"hello OR dogs'  ->  'cat' | 'say' & 'hello' <2> 'dog'
//	'cats OR -known'             ->  'cat' | !'known'
//
// The first silently turns the final OR into an AND, and the second turns a
// search term into a negation, so a query excludes exactly what it asked for.
// Both are reachable, because extractKeywords strips punctuation only at a
// token's edges so that `node.js` and `well-known` survive intact.
func TestSearchByText_TermsCarryingSearchSyntaxAreStillJustTerms(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	cats := storeMemory(t, repo, ctx, newMemory(projectID, "the cats sleep on the windowsill"))
	dogs := storeMemory(t, repo, ctx, newMemory(projectID, "the dogs bark at the postman"))

	// A term that lexes into two lexemes sits between the others. Under the
	// broken form its internal AND reached across the following OR and the
	// dogs memory was lost.
	got, err := repo.SearchByText(ctx, projectID, []string{"cats", `say"hello`, "dogs"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	matched := make(map[uuid.UUID]bool, len(got))
	for _, r := range got {
		matched[r.Memory.ID] = true
	}
	if !matched[cats.ID] || !matched[dogs.ID] {
		t.Errorf("a term containing a quote changed how the terms around it "+
			"combine: got %d results, want both memories", len(got))
	}

	// A leading dash must not negate. Under the broken form this query
	// excluded the cats memory rather than looking for it.
	got, err = repo.SearchByText(ctx, projectID, []string{"windowsill", "-cats"}, 10)
	if err != nil {
		t.Fatalf("search with a leading dash: %v", err)
	}
	found := false
	for _, r := range got {
		if r.Memory.ID == cats.ID {
			found = true
		}
	}
	if !found {
		t.Error("a term beginning with `-` was read as a negation, so the query " +
			"excluded the memory it was asking for")
	}
}

// Stop words among the terms must be dropped rather than emptying the whole
// tsquery, or one common word in a question would silently disable keyword
// retrieval for it.
func TestSearchByText_StopWordsAmongRealTermsAreIgnored(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "the zqxjklmw marker is recorded here"))

	got, err := repo.SearchByText(ctx, projectID, []string{"the", "zqxjklmw", "of"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Memory.ID != mem.ID {
		t.Errorf("a query mixing stop words with a real term returned %d results; "+
			"the stop words must drop out, not empty the query", len(got))
	}
}

// TestSearchByText_WeightsTermsByHowRareTheyAre is the acceptance criterion
// this feature exists for, and the one ts_rank_cd does not deliver on its own.
//
// ts_rank_cd measures term frequency and cover density and has no inverse
// document frequency at all: verified against Postgres 18, a term appearing in
// one document of the corpus and one appearing in 1,775 both rank 0.1 on an
// equivalent document. The apparent weighting from a query for `the` is the
// stop-word dictionary, not weighting, and it says nothing about the ordinary
// words a question is made of.
//
// Both memories below match exactly one query term, in text of the same
// length, so ts_rank_cd scores them identically. Only IDF separates them.
func TestSearchByText_WeightsTermsByHowRareTheyAre(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// `common` appears in every memory of the project; `zqxjklmw` in one.
	rare := storeMemory(t, repo, ctx, newMemory(projectID, "the ledger records zqxjklmw here"))
	commonOnly := storeMemory(t, repo, ctx, newMemory(projectID, "the ledger records widespread items"))
	for i := 0; i < 25; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("the ledger records widespread entry number %d", i)))
	}

	got, err := repo.SearchByText(ctx, projectID, []string{"zqxjklmw", "widespread"}, 30)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected both memories, got %d", len(got))
	}

	if got[0].Memory.ID != rare.ID {
		t.Errorf("the top hit is %q; the memory matching the term that appears "+
			"once must outrank one matching a term that appears 26 times. "+
			"ts_rank_cd alone scores these identically, which is why the ranking "+
			"weights each term by its inverse document frequency",
			got[0].Memory.Content)
	}

	// And by a real margin, not a rounding difference.
	var commonScore float64
	for _, r := range got {
		if r.Memory.ID == commonOnly.ID {
			commonScore = r.Score
		}
	}
	if commonScore > 0 && got[0].Score < commonScore*1.5 {
		t.Errorf("the rare-term hit scored %v against %v for the common-term hit; "+
			"the weighting is present but too weak to order results",
			got[0].Score, commonScore)
	}
}

// Tags are searchable text, not just filter labels.
//
// The retriever this replaces matched `content OR tags`. Dropping tags would
// make a memory findable by every word of its prose but not by the deliberate
// label someone attached to it, which is a regression whose only symptom is
// worse results.
func TestSearchByText_MatchesTags(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	tagged := storeMemory(t, repo, ctx,
		newMemory(projectID, "the rollout finished without incident", "kubernetes", "postgres"))
	storeMemory(t, repo, ctx, newMemory(projectID, "an unrelated note about lunch"))

	got, err := repo.SearchByText(ctx, projectID, []string{"kubernetes"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Memory.ID != tagged.ID {
		t.Errorf("a search for a term appearing only in a memory's tags returned "+
			"%d results; tags are a deliberate label and must stay searchable", len(got))
	}

	// The memory's tags survive the round trip, so the caller still sees them.
	if len(got) > 0 && len(got[0].Memory.Tags) != 2 {
		t.Errorf("the hydrated memory has %d tags, want 2", len(got[0].Memory.Tags))
	}
}

// The cost of a keyword search must not scale with how many memories match it.
//
// This is the defect that took query latency from 8.9ms to 237.8ms in CI and
// left main red for two days, and neither the unit tests nor the golden
// retrieval suite could see it: the query returned exactly the right rows the
// whole time. Only the plan says which.
//
// Two scalar facts about the corpus -- its size, and each term's document
// frequency -- were written as ordinary CTEs. PostgreSQL inlines a CTE
// referenced once, so the planner hung them off the join over matching rows
// and re-executed both per candidate. Measured at 4,000 memories: 266
// candidates, 266 executions of each, 489ms for one query.
//
// The assertion is on loop counts rather than on elapsed time, because a
// timing threshold on a shared CI runner is a flake generator, and because the
// number of executions is the actual claim: constant work per query, not work
// per matching row.
func TestSearchByKeywords_CostDoesNotScaleWithMatchCount(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// Enough matches that a per-row re-execution is unmistakable in the loop
	// counts, while staying small enough to seed quickly.
	const matches = 60
	for i := 0; i < matches; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID,
			fmt.Sprintf("release %d shipped the kubernetes rollout and the postgres migration", i)))
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	sql, args := keywordSearchQuery(projectID, []string{"kubernetes", "postgres", "migration"}, 100)

	// enable_seqscan=off for the same reason TestSearchByText_UsesTheFullTextIndex
	// uses it: at test size a sequential scan is genuinely the cheapest plan, so
	// seeing one proves nothing. Under this setting a sequential scan means no
	// index can serve the predicate at all, which is the claim being tested.
	// Materialisation is unaffected by the setting, so the loop counts below are
	// measured on the same plan.
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	var plan string
	row := tx.QueryRow(ctx,
		`EXPLAIN (ANALYZE, FORMAT JSON, TIMING OFF, SUMMARY OFF) `+sql, args...)
	if err := row.Scan(&plan); err != nil {
		t.Fatalf("explain: %v", err)
	}

	// Loop counts for the nodes that read the Memory label, flattened. The
	// planner is free to reshape the query, so the test asserts a property of
	// whatever plan it chose rather than the shape of one particular plan.
	//
	// Restricted to nodes touching Memory on purpose. A healthy plan re-enters
	// the single-row `corpus` CTE once per output row, which is free and says
	// nothing; work against the label is what has to stay bounded.
	var maxLoops float64
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if node["Relation Name"] == "Memory" {
			if loops, ok := node["Actual Loops"].(float64); ok && loops > maxLoops {
				maxLoops = loops
			}
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, c := range children {
				if child, ok := c.(map[string]any); ok {
					walk(child)
				}
			}
		}
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(plan), &parsed); err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	for _, p := range parsed {
		if node, ok := p["Plan"].(map[string]any); ok {
			walk(node)
		}
	}

	// One execution per query term is expected and bounded by the query, not
	// by the data. Anything approaching the match count is the defect.
	const perTermCeiling = 10
	if maxLoops > perTermCeiling {
		t.Errorf("a scan of the Memory label ran %v times for a query with 3 terms and %d matching memories; "+
			"the cost of a keyword search is scaling with how many rows match, "+
			"which is the defect MATERIALIZED exists to prevent:\n%s",
			maxLoops, matches, plan)
	}

	// The project filter has its own index precisely because the SQL spells it
	// with a ::text cast that the agtype-form index cannot serve. Without it
	// the corpus count sequential-scans the whole label, which no loop count
	// would reveal.
	if strings.Contains(plan, `"Node Type": "Seq Scan"`) {
		t.Errorf("the keyword search sequential-scans the Memory label:\n%s\n"+
			"the project filter's index is missing or spelled differently from the query", plan)
	}
}
