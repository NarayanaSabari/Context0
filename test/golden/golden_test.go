// Package golden holds the retrieval regression suite.
//
// It answers one question that no other test in this repository answers: did a
// change to ranking, extraction, or the graph make retrieval worse? Until this
// existed, the only evidence for that was a LoCoMo score from an out-of-tree
// benchmark, at n=40, through a non-deterministic judge -- which meant a
// retrieval regression could reach main and nothing would say so.
//
// The suite stores a fixed corpus through the real Store path (embeddings,
// entity links and auto-linking included), runs fixed queries through the real
// Query path, and scores where the known answer landed. It asserts floors on
// recall@k and MRR, overall and per group.
//
// It is hermetic in everything but the database: the default bag-of-words
// embedder makes no network call, and no LLM is involved. The numbers it
// produces are therefore a floor on quality, not a claim about it. A
// deployment with a real embedding model does better, and the thresholds here
// must not be read as a measure of what Kora can do -- only as a tripwire for
// what it stops doing. Point it at a real embedder with
// KORA_TEST_EMBEDDING_PROVIDER (see newService) before a benchmark run, when
// the question is quality rather than regression.
//
// # Which retrievers this actually guards
//
// Group names describe the shape of a query, not which retriever answered it,
// so the only honest way to know what the suite protects is to delete a
// retriever and see whether it fails. Measured that way, on this corpus:
//
//   - Delete entity retrieval and the suite fails: `What is Will responsible
//     for?` becomes unreachable, taking subject recall to 0.909 and overall
//     to 0.879, both under their floors. That one case carries the entity
//     hop on its own. It works because "Will" is a PostgreSQL stop word, so
//     full-text search cannot see the name in the query or in the memory,
//     and "responsible" appears nowhere in the corpus. Only the entity node
//     connects them.
//   - Delete vector retrieval and the suite still passes. Seven cases move,
//     four of them for the better, and no floor breaks.
//
// So the vector retriever is not guarded here, and saying so is the point:
// with the offline bag-of-words embedder there is no paraphrase this corpus
// could pose that vectors answer and full-text search does not, because that
// embedder scores token overlap rather than meaning. Guarding it needs a
// corpus with real restatements and a real embedder, which is issue #67.
//
// Skipped unless KORA_TEST_DATABASE_URL is set, so `go test ./...` stays
// hermetic:
//
//	docker compose up -d postgres
//	KORA_TEST_DATABASE_URL="postgres://kora:$POSTGRES_PASSWORD@localhost:5432/kora?sslmode=disable" \
//	  go test ./test/golden/...
package golden

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// topK is the retrieval budget every case is scored at. It is the number an
// agent would realistically put in an LLM context window, and it is what
// recall@k and MRR below are measured at. Raising it makes every threshold
// here meaningless until they are re-measured.
const topK = 10

// embeddingDim must match the width of the memory_embeddings column the server
// creates by default, since this suite shares one database with it.
const embeddingDim = 384

// Thresholds, measured on the corpus in golden.json with the bag-of-words
// embedder. Most are set roughly one lost case below the measured value. The
// two recall floors that sit at 1.000 are pinned exactly at what was measured,
// deliberately: losing a single lexical or subject case is a regression worth
// failing on, because full-text search answers every one of them today.
//
// They are this tight because the suite is deterministic: ten consecutive runs
// produced identical numbers to three decimal places. That holds while every
// retriever's candidate pool is larger than the corpus, so ranking sees the
// whole set and never has to break a tie on the arbitrary order AGE returns.
// Shrink a pool and the numbers start moving between runs -- which is itself
// worth knowing, and is how the pool sizes were confirmed to matter.
//
// Measured over 33 cases: overall recall@10 0.909, MRR 0.828; lexical
// 1.000 / 0.958; paraphrase 0.700 / 0.583; subject 1.000 / 0.909.
//
// They are floors, not targets. Raise them when a change genuinely improves
// retrieval, so the gain cannot be given back silently. Never lower one to
// make a failing build pass without saying, in the commit message, which
// behaviour was traded away and why.
const (
	minRecall = 0.90
	minMRR    = 0.80
)

// groups are the case groups, their floors, and the single place any of the
// three names is written in Go. A case whose group is not one of these fails
// the run rather than quietly scoring in the overall figure and nowhere else,
// which is what a typo in golden.json used to buy.
var groups = []struct {
	name        string
	recall, mrr float64
}{
	// Queries sharing distinctive words with their answer.
	{name: "lexical", recall: 1.00, mrr: 0.90},

	// The paraphrase group is the weakest by construction: these queries share
	// few words with their answers, so they lean on the embedder, and the
	// offline bag-of-words embedder scores token overlap rather than meaning.
	// Three of its ten cases miss entirely for that reason. This floor guards
	// the fallback behaviour, not semantic understanding; with a real embedder
	// configured it should be raised.
	// Queries asking for the same thing in different words.
	{name: "paraphrase", recall: 0.70, mrr: 0.55},

	// Queries naming a person or service that several memories mention.
	{name: "subject", recall: 1.00, mrr: 0.85},
}

type goldenSet struct {
	Corpus []struct {
		Label   string `json:"label"`
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"corpus"`
	Cases []struct {
		Group  string `json:"group"`
		Query  string `json:"query"`
		Expect string `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) goldenSet {
	t.Helper()

	raw, err := os.ReadFile("golden.json")
	if err != nil {
		t.Fatalf("read golden.json: %v", err)
	}
	var gs goldenSet
	if err := json.Unmarshal(raw, &gs); err != nil {
		t.Fatalf("parse golden.json: %v", err)
	}
	if len(gs.Cases) < 30 {
		t.Fatalf("golden set has %d cases; a set this small cannot distinguish a "+
			"regression from noise", len(gs.Cases))
	}

	// A case in an unknown group would be scored in the overall figure and in
	// no group floor, so a typo would quietly weaken the suite.
	known := make(map[string]bool, len(groups))
	for _, g := range groups {
		known[g.name] = true
	}
	for _, c := range gs.Cases {
		if !known[c.Group] {
			t.Fatalf("case %q is in group %q, which has no floor in golden_test.go", c.Query, c.Group)
		}
	}

	return gs
}

func memoryType(t *testing.T, name string) pb.MemoryType {
	t.Helper()

	switch name {
	case "episodic":
		return pb.MemoryType_MEMORY_TYPE_EPISODIC
	case "semantic":
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC
	case "procedural":
		return pb.MemoryType_MEMORY_TYPE_PROCEDURAL
	default:
		t.Fatalf("golden.json names memory type %q, which the engine does not have", name)
		return pb.MemoryType_MEMORY_TYPE_UNSPECIFIED
	}
}

// newService connects to the test database and returns a MemoryService wired
// exactly as the server wires it, minus the LLM extractor: this suite scores
// retrieval, and an extraction call would put a network dependency and a
// non-deterministic model in the middle of it.
//
// The embedder defaults to bag-of-words so CI needs no credentials and no
// network. KORA_TEST_EMBEDDING_PROVIDER, _MODEL, _API_KEY, _BASE_URL and _DIM
// override it, using the same names and meanings as the server's own
// settings. A provider whose dimension differs from the default needs its own
// database: the embedding column is created at the width first seen.
func newService(t *testing.T) (*service.MemoryService, context.Context) {
	t.Helper()

	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set; skipping the golden retrieval suite")
	}

	ctx := context.Background()

	// A container that has just started accepts TCP connections before it can
	// serve queries, and CI starts PostgreSQL moments before this runs.
	var pool *pgxpool.Pool
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		if pool, err = graph.NewPool(ctx, dsn); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("connect to test database after retries: %v", err)
	}
	t.Cleanup(pool.Close)

	dim := embeddingDim
	if v := os.Getenv("KORA_TEST_EMBEDDING_DIM"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			t.Fatalf("KORA_TEST_EMBEDDING_DIM=%q is not a positive integer", v)
		}
		dim = parsed
	}

	embedder, err := embedding.NewFromConfig(embedding.ProviderConfig{
		Provider: os.Getenv("KORA_TEST_EMBEDDING_PROVIDER"),
		Model:    os.Getenv("KORA_TEST_EMBEDDING_MODEL"),
		APIKey:   os.Getenv("KORA_TEST_EMBEDDING_API_KEY"),
		BaseURL:  os.Getenv("KORA_TEST_EMBEDDING_BASE_URL"),
		Dim:      dim,
	})
	if err != nil {
		t.Fatalf("build embedder: %v", err)
	}

	repo := graph.NewAGERepository(pool, dim)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	return service.NewMemoryService(repo, embedder), ctx
}

// outcome is where one case's known answer landed.
type outcome struct {
	group string
	query string
	want  string
	rank  int // 1-based; 0 means the answer was not in the top-K
	top   string
}

// TestGoldenRetrieval is the regression tripwire: it stores the corpus, runs
// every case, and fails when recall@k or MRR falls below the committed floors.
func TestGoldenRetrieval(t *testing.T) {
	svc, ctx := newService(t)
	gs := load(t)

	// A unique project per run, so a leftover corpus from an interrupted run
	// cannot silently join this one and change every rank in it.
	projectID := "golden-" + uuid.NewString()

	idOf := make(map[string]string, len(gs.Corpus))
	labelOf := make(map[string]string, len(gs.Corpus))
	for _, m := range gs.Corpus {
		resp, err := svc.Store(ctx, &pb.StoreRequest{
			Content:   m.Content,
			Type:      memoryType(t, m.Type),
			ProjectId: projectID,
		})
		if err != nil {
			t.Fatalf("store %q: %v", m.Label, err)
		}
		idOf[m.Label] = resp.Memory.Id
		labelOf[resp.Memory.Id] = m.Label
	}

	t.Cleanup(func() {
		for _, id := range idOf {
			if _, err := svc.Delete(context.Background(), &pb.DeleteRequest{Id: id}); err != nil {
				t.Logf("cleanup: deleting %s failed: %v", id, err)
			}
		}
	})

	outcomes := make([]outcome, 0, len(gs.Cases))
	for _, c := range gs.Cases {
		want, ok := idOf[c.Expect]
		if !ok {
			t.Fatalf("case %q expects label %q, which is not in the corpus", c.Query, c.Expect)
		}

		resp, err := svc.Query(ctx, &pb.QueryRequest{
			Query:     c.Query,
			ProjectId: projectID,
			TopK:      topK,
		})
		if err != nil {
			t.Fatalf("query %q: %v", c.Query, err)
		}

		o := outcome{group: c.Group, query: c.Query, want: c.Expect}
		for i, r := range resp.Results {
			if i == 0 {
				o.top = labelOf[r.Memory.Id]
			}
			if r.Memory.Id == want {
				o.rank = i + 1
				break
			}
		}
		outcomes = append(outcomes, o)
	}

	report(t, outcomes)

	overall := score(outcomes, "")
	checkFloor(t, "overall", overall, minRecall, minMRR)

	for _, g := range groups {
		checkFloor(t, g.name, score(outcomes, g.name), g.recall, g.mrr)
	}
}

type result struct {
	cases  int
	recall float64
	mrr    float64
}

func score(outcomes []outcome, group string) result {
	var r result
	for _, o := range outcomes {
		if group != "" && o.group != group {
			continue
		}
		r.cases++
		if o.rank > 0 {
			r.recall++
			r.mrr += 1 / float64(o.rank)
		}
	}
	if r.cases == 0 {
		return r
	}
	r.recall /= float64(r.cases)
	r.mrr /= float64(r.cases)
	return r
}

// checkFloor names its floors floorRecall and floorMRR rather than reusing the
// package constants' names, which they would shadow: an edit inside the body
// would then read the parameter while looking like it read the constant.
func checkFloor(t *testing.T, name string, got result, floorRecall, floorMRR float64) {
	t.Helper()

	if got.cases == 0 {
		t.Errorf("%s: no cases ran; the group was renamed or removed without updating the thresholds", name)
		return
	}
	if got.recall < floorRecall {
		t.Errorf("%s: recall@%d = %.3f over %d cases, below the floor of %.3f: "+
			"a memory that used to be retrievable no longer is",
			name, topK, got.recall, got.cases, floorRecall)
	}
	if got.mrr < floorMRR {
		t.Errorf("%s: MRR = %.3f over %d cases, below the floor of %.3f: "+
			"the right answers are still retrieved but ranked lower",
			name, got.mrr, got.cases, floorMRR)
	}
}

// report prints every case's rank, so a failure names the queries that moved
// rather than only the aggregate that fell.
func report(t *testing.T, outcomes []outcome) {
	t.Helper()

	sorted := make([]outcome, len(outcomes))
	copy(sorted, outcomes)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].group != sorted[j].group {
			return sorted[i].group < sorted[j].group
		}
		return sorted[i].rank > sorted[j].rank
	})

	var b []byte
	b = append(b, "\nrank  group     expected              query\n"...)
	for _, o := range sorted {
		rank := fmt.Sprintf("%4d", o.rank)
		if o.rank == 0 {
			rank = "MISS"
		}
		line := fmt.Sprintf("%s  %-9s %-21s %s", rank, o.group, o.want, o.query)
		if o.rank == 0 && o.top != "" {
			// What came back instead is the first thing worth knowing about a
			// miss: a plausible neighbour means ranking, an unrelated memory
			// means retrieval.
			line += fmt.Sprintf("  [top: %s]", o.top)
		}
		b = append(b, (line + "\n")...)
	}

	names := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		names = append(names, g.name)
	}
	for _, g := range append(names, "") {
		s := score(outcomes, g)
		name := g
		if name == "" {
			name = "overall"
		}
		b = append(b, fmt.Sprintf("%-10s cases=%2d recall@%d=%.3f MRR=%.3f\n",
			name, s.cases, topK, s.recall, s.mrr)...)
	}
	t.Log(string(b))
}
