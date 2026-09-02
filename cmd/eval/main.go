// Command eval is the offline retrieval evaluation behind `make eval`.
//
// Two subcommands:
//
//	eval fixtures   build the embedding fixture, once, from a local Ollama
//	eval run        load a corpus into an empty database, run the pinned
//	                questions through the real retrieval engine, and score
//	                the ranked lists against LoCoMo's evidence annotations
//
// `run` makes no network call and consults no model: query and corpus
// vectors come from the fixture, and a text the fixture lacks fails the run
// rather than degrading it. See eval/README.md for what is measured and why.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/evalset"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/retrieval"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultDataDir     = "eval/data"
	defaultFixturesDir = "eval/fixtures/locomo"
	dsnEnv             = "KORA_EVAL_DATABASE_URL"
	nomicDim           = 768
)

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "fixtures":
		err = runFixtures(args)
	case "run":
		err = runEval(args)
	default:
		err = fmt.Errorf("unknown command %q (want fixtures or run)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
}

// paths groups the file locations both subcommands share.
type paths struct {
	dataset, pinned, sha, embeddings, snapshot, labels string
}

func newPaths(dataDir, fixturesDir string) paths {
	return paths{
		dataset:    filepath.Join(dataDir, "locomo10.json"),
		snapshot:   filepath.Join(dataDir, "corpus-extracted.json"),
		pinned:     filepath.Join(fixturesDir, "pinned.json"),
		sha:        filepath.Join(fixturesDir, "locomo10.sha256"),
		embeddings: filepath.Join(fixturesDir, "embeddings.bin"),
		labels:     filepath.Join(fixturesDir, "labels-extracted.json"),
	}
}

// loadDataset reads the pinned questions and, when the fixture records a
// checksum, refuses a dataset file that does not match it: the numbers are
// only comparable across runs if every run read the same bytes.
func loadDataset(p paths) (*evalset.Dataset, error) {
	pinned, err := evalset.LoadPinned(p.pinned)
	if err != nil {
		return nil, fmt.Errorf("pinned question ids: %w", err)
	}
	ds, err := evalset.Load(p.dataset, pinned)
	if err != nil {
		return nil, fmt.Errorf("dataset: %w (fetch it once with `go run ./cmd/eval fixtures`)", err)
	}
	if want, err := os.ReadFile(p.sha); err == nil {
		if got := ds.SHA256; got != strings.TrimSpace(string(want)) {
			return nil, fmt.Errorf("%s has sha256 %s, fixture expects %s", p.dataset, got, strings.TrimSpace(string(want)))
		}
	}
	return ds, nil
}

// --- fixtures ---

func runFixtures(args []string) error {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	dataDir := fs.String("data", defaultDataDir, "directory for the downloaded dataset and snapshot")
	fixturesDir := fs.String("fixtures", defaultFixturesDir, "directory of committed fixtures")
	ollama := fs.String("ollama", "http://localhost:11434", "Ollama base URL, used once to embed")
	model := fs.String("model", "nomic-embed-text", "Ollama embedding model")
	dim := fs.Int("dim", nomicDim, "vector width")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p := newPaths(*dataDir, *fixturesDir)

	if err := ensureDataset(p.dataset); err != nil {
		return err
	}
	pinned, err := evalset.LoadPinned(p.pinned)
	if err != nil {
		return err
	}
	ds, err := evalset.Load(p.dataset, pinned)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.sha); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(p.sha, []byte(ds.SHA256+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "recorded dataset sha256 %s\n", ds.SHA256)
	} else if want, _ := os.ReadFile(p.sha); strings.TrimSpace(string(want)) != ds.SHA256 {
		return fmt.Errorf("dataset sha256 %s does not match the recorded %s", ds.SHA256, strings.TrimSpace(string(want)))
	}

	texts := make([]string, 0, len(ds.Questions)+len(ds.Turns))
	for _, q := range ds.Questions {
		texts = append(texts, q.Question)
	}
	for _, t := range ds.Turns {
		texts = append(texts, t.Content)
	}
	if docs, err := evalset.LoadSnapshot(p.snapshot); err == nil {
		for _, d := range docs {
			texts = append(texts, d.Content)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	emb, err := evalset.ReadEmbeddings(p.embeddings)
	if errors.Is(err, os.ErrNotExist) {
		emb = evalset.NewEmbeddings(*dim)
	} else if err != nil {
		return err
	}
	if emb.Dim() != *dim {
		return fmt.Errorf("fixture is %d-dimensional, asked for %d", emb.Dim(), *dim)
	}

	var missing []string
	seen := make(map[string]bool, len(texts))
	for _, t := range texts {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, ok := emb.Lookup(t); !ok {
			missing = append(missing, t)
		}
	}
	fmt.Fprintf(os.Stderr, "fixture holds %d vectors; %d of %d texts need embedding\n", emb.Len(), len(missing), len(seen))
	if len(missing) == 0 {
		return nil
	}

	// The engine's own client, so the fixture holds exactly the vectors the
	// server would have produced.
	embedder := embedding.NewOllamaEmbedder(*ollama, *model, *dim)
	for i, text := range missing {
		vec, err := embedder.Embed(text)
		if err != nil {
			return fmt.Errorf("embed text %d: %w", i, err)
		}
		if err := emb.Add(text, vec); err != nil {
			return err
		}
		if (i+1)%500 == 0 || i+1 == len(missing) {
			fmt.Fprintf(os.Stderr, "  embedded %d/%d\n", i+1, len(missing))
			if err := emb.Write(p.embeddings); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %s with %d vectors\n", p.embeddings, emb.Len())
	return nil
}

func ensureDataset(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "downloading %s\n", evalset.DatasetURL)
	resp, err := http.Get(evalset.DatasetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download dataset: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// --- run ---

// Report is what one evaluation run writes.
type Report struct {
	Timestamp    time.Time `json:"timestamp"`
	Commit       string    `json:"commit"`
	Corpus       string    `json:"corpus"`
	Embedder     string    `json:"embedder"`
	GraphSignals string    `json:"graph_signals"`
	Fusion       string    `json:"fusion"`
	TopK         int       `json:"top_k"`
	Passes       int       `json:"passes"`
	Dataset      struct {
		SHA256    string `json:"sha256"`
		Turns     int    `json:"turns"`
		Questions int    `json:"questions"`
	} `json:"dataset"`
	Load struct {
		Memories int   `json:"memories"`
		Entities int   `json:"entity_links"`
		Ms       int64 `json:"ms"`
	} `json:"load"`
	Metrics map[string]evalset.Aggregate `json:"metrics"`
	Latency struct {
		Queries int     `json:"queries"`
		P50     float64 `json:"p50_ms"`
		P95     float64 `json:"p95_ms"`
		P99     float64 `json:"p99_ms"`
		Mean    float64 `json:"mean_ms"`
		Max     float64 `json:"max_ms"`
	} `json:"latency"`
	Allocs struct {
		MallocsPerQuery float64 `json:"mallocs_per_query"`
		BytesPerQuery   float64 `json:"bytes_per_query"`
		HeapInuse       uint64  `json:"heap_inuse_bytes"`
	} `json:"allocs"`
	Index map[string]int64 `json:"index_bytes"`
	// Stages is the mean time per query spent in each read-path step during
	// the scoring pass, in milliseconds.
	Stages    map[string]float64 `json:"stage_ms"`
	Digest    string             `json:"digest"`
	Questions []questionReport   `json:"questions"`
}

type questionReport struct {
	ID                string   `json:"id"`
	Category          string   `json:"category"`
	EvidenceAnnotated int      `json:"evidence_annotated"`
	EvidencePresent   int      `json:"evidence_present"`
	Ranks             []int    `json:"ranks"`
	Retrieved         []string `json:"retrieved"`
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv(dsnEnv), "empty PostgreSQL+AGE+pgvector database (default: $"+dsnEnv+", which scripts/eval_db.sh up prints)")
	dataDir := fs.String("data", defaultDataDir, "directory holding the dataset and snapshot")
	fixturesDir := fs.String("fixtures", defaultFixturesDir, "directory of committed fixtures")
	corpusName := fs.String("corpus", "turns", "turns (verbatim utterances, exact labels) or extracted (snapshot of the 0.690 run)")
	embedderName := fs.String("embedder", "fixture", "fixture (frozen nomic-embed-text vectors) or bow (the install default)")
	topK := fs.Int("topk", 30, "results per query, the benchmark adapter's budget")
	passes := fs.Int("passes", 3, "query passes; the first scores, the rest time")
	graphSignals := fs.String("graph-signals", "on", "on, or off for the FTS+vector ablation")
	out := fs.String("out", "", "report path (default eval/results/<corpus>-<embedder>.json)")
	withTrace := fs.Bool("trace", false, "also write a per-question retrieval trace beside the report, as <report>-trace.json")
	fusionMode := fs.String("fusion", string(retrieval.DefaultFusion().Mode), "linear, minmax or rrf; see retrieval.Fusion")
	weights := fs.String("weights", "", "keyword,semantic,entity fusion weights (default: the engine's)")
	rrfK := fs.Float64("rrf-k", 60, "reciprocal rank fusion constant")
	coverage := fs.Float64("coverage", -1, "keyword coverage exponent in [0,1]; default: the engine's")
	reuseDB := fs.Bool("reuse-db", false, "reuse a database already holding this corpus instead of demanding an empty one")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile of the timing passes to this file")
	memProfile := fs.String("memprofile", "", "write an allocation profile taken after the timing passes to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *passes < 1 {
		return errors.New("-passes must be at least 1")
	}
	if *dsn == "" {
		return errors.New("no database: set " + dsnEnv + " or pass -dsn; scripts/eval_db.sh up starts one and prints its URL")
	}
	// top_k is an int32 on the wire; the engine clamps it to 200, and asking
	// for more here would only be truncated there.
	if *topK < 1 || *topK > 200 {
		return fmt.Errorf("-topk must be between 1 and 200, got %d", *topK)
	}
	topK32 := int32(*topK)
	p := newPaths(*dataDir, *fixturesDir)
	ctx := context.Background()

	ds, err := loadDataset(p)
	if err != nil {
		return err
	}

	var corpus *evalset.Corpus
	switch *corpusName {
	case "turns":
		corpus = evalset.TurnsCorpus(ds)
	case "extracted":
		corpus, err = evalset.ExtractedCorpus(p.snapshot, p.labels)
		if err != nil {
			return fmt.Errorf("extracted corpus: %w (the snapshot is a local file; see eval/README.md)", err)
		}
	default:
		return fmt.Errorf("unknown corpus %q", *corpusName)
	}

	var embedder embedding.Embedder
	var fixture *evalset.FixtureEmbedder
	switch *embedderName {
	case "fixture":
		emb, err := evalset.ReadEmbeddings(p.embeddings)
		if err != nil {
			return fmt.Errorf("embedding fixture: %w", err)
		}
		fixture = evalset.NewFixtureEmbedder(emb)
		embedder = fixture
	case "bow":
		embedder = embedding.NewBagOfWordsEmbedder(384)
	default:
		return fmt.Errorf("unknown embedder %q", *embedderName)
	}

	pool, err := graph.NewPool(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w (start one with scripts/eval_db.sh up)", err)
	}
	defer pool.Close()
	repo := graph.NewAGERepository(pool, embedder.Dimension())
	if err := repo.InitSchema(ctx); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	// A marker names the corpus a database holds, so a sweep over ranking
	// variants can skip the load. Safe because Retrieve mutates nothing: the
	// read path does not touch access counts (that is Query's job), so the
	// tables are the same on the hundredth run as on the first.
	marker := fmt.Sprintf("%s/%s/%d/%s", corpus.Name, *embedderName, len(corpus.Docs), ds.SHA256)
	loaded, err := corpusMarker(ctx, pool)
	if err != nil {
		return err
	}
	var stats evalset.LoadStats
	switch {
	case loaded == marker && *reuseDB:
		logf("reusing loaded corpus %s", corpus.Name)
		stats.Memories = len(corpus.Docs)
	case loaded != "":
		return fmt.Errorf("database already holds corpus %q; pass -reuse-db to keep it or recreate it with scripts/eval_db.sh up", loaded)
	default:
		if n, err := repo.NodeCount(ctx); err != nil {
			return err
		} else if n > 0 {
			return fmt.Errorf("database already holds %d nodes; the eval needs an empty one (scripts/eval_db.sh up recreates it)", n)
		}
		logf("loading corpus %s: %d docs", corpus.Name, len(corpus.Docs))
		stats, err = evalset.LoadCorpus(ctx, repo, corpus, embedder, func(done, total int) {
			logf("  %d/%d", done, total)
		})
		if err != nil {
			return err
		}
		// Fresh planner statistics, so the latency numbers reflect the plans
		// a long-running deployment would use rather than a table Postgres
		// has never looked at.
		if _, err := pool.Exec(ctx, "ANALYZE"); err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		if err := setCorpusMarker(ctx, pool, marker); err != nil {
			return err
		}
		logf("loaded %d memories, %d entity links in %s", stats.Memories, stats.Entities, stats.Duration.Round(time.Millisecond))
	}

	engine := retrieval.New(repo, embedder)
	engine.SetClock(func() time.Time { return corpus.Clock })
	fusion := retrieval.DefaultFusion()
	fusion.Mode = retrieval.FusionMode(*fusionMode)
	fusion.RRFK = *rrfK
	if *coverage >= 0 {
		fusion.Coverage = *coverage
	}
	if *weights != "" {
		if _, err := fmt.Sscanf(*weights, "%g,%g,%g", &fusion.Keyword, &fusion.Semantic, &fusion.Entity); err != nil {
			return fmt.Errorf("-weights wants three comma-separated numbers, got %q", *weights)
		}
	}
	if err := engine.SetFusion(fusion); err != nil {
		return err
	}
	switch *graphSignals {
	case "on":
	case "off":
		engine.DisableGraphSignals()
	default:
		return fmt.Errorf("-graph-signals must be on or off, got %q", *graphSignals)
	}

	sources := corpus.Sources()
	present := corpus.Present()
	results := make([]evalset.QuestionResult, 0, len(ds.Questions))
	var durations []time.Duration
	digest := sha256.New()

	docByID := make(map[uuid.UUID]evalset.Doc, len(corpus.Docs))
	for _, d := range corpus.Docs {
		docByID[d.ID] = d
	}
	var traces []questionTrace
	stageTotals := make(map[string]time.Duration)

	// Pass 1 scores. Its timings count only when it is the only pass, since
	// a cold cache and a cold pool are a property of the run, not the engine.
	for _, q := range ds.Questions {
		start := time.Now()
		hits, trace, err := engine.RetrieveTraced(ctx, q.Question, evalset.ProjectID(q.Conversation), nil, topK32)
		elapsed := time.Since(start)
		if err != nil {
			return fmt.Errorf("query %s: %w", q.ID, err)
		}
		for name, d := range trace.Stages {
			stageTotals[name] += d
		}
		if *withTrace {
			traces = append(traces, buildTrace(q, trace, docByID, present))
		}
		ids := make([]uuid.UUID, len(hits))
		fmt.Fprintf(digest, "%s:", q.ID)
		for i, h := range hits {
			ids[i] = h.Memory.ID
			fmt.Fprintf(digest, "%s,", h.Memory.ID)
		}
		fmt.Fprintln(digest)
		r := evalset.Score(q, ids, sources, present)
		r.Latency = elapsed
		results = append(results, r)
		if *passes == 1 {
			durations = append(durations, elapsed)
		}
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if *cpuProfile != "" {
		f, err := os.Create(filepath.Clean(*cpuProfile))
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if err := pprof.StartCPUProfile(f); err != nil {
			return fmt.Errorf("cpu profile: %w", err)
		}
		defer pprof.StopCPUProfile()
	}
	if *memProfile != "" {
		// Every allocation of the timing passes, not a sample: the question
		// is where the 36k mallocs per query come from, and a sampled profile
		// of a 400-query run would miss the small ones that make up most of
		// them.
		runtime.MemProfileRate = 1
	}
	timed := 0
	for pass := 2; pass <= *passes; pass++ {
		for _, q := range ds.Questions {
			start := time.Now()
			if _, err := engine.Retrieve(ctx, q.Question, evalset.ProjectID(q.Conversation), nil, topK32); err != nil {
				return fmt.Errorf("query %s (pass %d): %w", q.ID, pass, err)
			}
			durations = append(durations, time.Since(start))
			timed++
		}
	}
	runtime.ReadMemStats(&after)
	if *memProfile != "" {
		f, err := os.Create(filepath.Clean(*memProfile))
		if err != nil {
			return err
		}
		if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
			_ = f.Close()
			return fmt.Errorf("alloc profile: %w", err)
		}
		if err := f.Close(); err != nil {
			return err
		}
	}

	if fixture != nil && fixture.Misses() > 0 {
		return fmt.Errorf("%d embedding lookups missed the fixture; the vector retriever was silently absent, so these numbers are void", fixture.Misses())
	}

	rep := Report{
		Timestamp:    time.Now().UTC(),
		Commit:       gitCommit(),
		Corpus:       corpus.Name,
		Embedder:     *embedderName,
		GraphSignals: *graphSignals,
		Fusion:       fmt.Sprintf("%s %g/%g/%g k=%g c=%g", fusion.Mode, fusion.Keyword, fusion.Semantic, fusion.Entity, fusion.RRFK, fusion.Coverage),
		TopK:         *topK,
		Passes:       *passes,
		Metrics:      make(map[string]evalset.Aggregate),
		Index:        make(map[string]int64),
		Stages:       make(map[string]float64),
		Digest:       hex.EncodeToString(digest.Sum(nil)),
	}
	for name, d := range stageTotals {
		rep.Stages[name] = float64(d.Microseconds()) / 1000 / float64(len(ds.Questions))
	}
	rep.Dataset.SHA256 = ds.SHA256
	rep.Dataset.Turns = len(ds.Turns)
	rep.Dataset.Questions = len(ds.Questions)
	rep.Load.Memories = stats.Memories
	rep.Load.Entities = stats.Entities
	rep.Load.Ms = stats.Duration.Milliseconds()

	ks := []int{1, 3, 5, 10, *topK}
	for name, group := range evalset.ByCategory(results) {
		rep.Metrics[name] = evalset.AggregateResults(group, ks)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	rep.Latency.Queries = len(durations)
	rep.Latency.P50 = percentile(durations, 0.50)
	rep.Latency.P95 = percentile(durations, 0.95)
	rep.Latency.P99 = percentile(durations, 0.99)
	rep.Latency.Max = percentile(durations, 1)
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	if len(durations) > 0 {
		rep.Latency.Mean = float64(total.Microseconds()) / float64(len(durations)) / 1000
	}
	if timed > 0 {
		rep.Allocs.MallocsPerQuery = float64(after.Mallocs-before.Mallocs) / float64(timed)
		rep.Allocs.BytesPerQuery = float64(after.TotalAlloc-before.TotalAlloc) / float64(timed)
	}
	rep.Allocs.HeapInuse = after.HeapInuse

	if err := indexSizes(ctx, pool, rep.Index); err != nil {
		return err
	}

	for _, r := range results {
		qr := questionReport{ID: r.ID, Category: r.Category, EvidenceAnnotated: r.EvidenceAnnotated,
			EvidencePresent: r.EvidencePresent, Ranks: r.Ranks}
		for _, id := range r.Retrieved {
			qr.Retrieved = append(qr.Retrieved, id.String())
		}
		rep.Questions = append(rep.Questions, qr)
	}

	if *out == "" {
		*out = filepath.Join("eval", "results", fmt.Sprintf("%s-%s.json", corpus.Name, *embedderName))
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Clean(*out), raw, 0o600); err != nil {
		return err
	}

	if *withTrace {
		raw, err := json.MarshalIndent(traces, "", " ")
		if err != nil {
			return err
		}
		// Named after the report rather than by a second path flag: the
		// trace is an artefact of this run, and one output location is one
		// fewer path to reason about.
		tracePath := strings.TrimSuffix(*out, ".json") + "-trace.json"
		if err := os.WriteFile(tracePath, raw, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "trace written to %s\n", tracePath)
	}

	printReport(os.Stdout, rep, ks)
	fmt.Fprintf(os.Stdout, "\nreport written to %s\n", *out)
	return nil
}

// questionTrace is the failure-analysis view of one query: where each piece
// of evidence stood in every retriever, and what outranked it.
type questionTrace struct {
	ID            string             `json:"id"`
	Category      string             `json:"category"`
	Question      string             `json:"question"`
	Answer        string             `json:"answer"`
	Keywords      []string           `json:"keywords"`
	Unsearchable  bool               `json:"unsearchable"`
	QueryEntities []string           `json:"query_entities"`
	PoolSizes     map[string]int     `json:"pool_sizes"`
	StageMs       map[string]float64 `json:"stage_ms"`
	Evidence      []evidenceTrace    `json:"evidence"`
	Top           []rankedTrace      `json:"top"`
}

type evidenceTrace struct {
	Key     string `json:"key"`
	Present bool   `json:"present"`
	// One entry per doc that is evidence for this turn.
	Docs []docTrace `json:"docs"`
}

type docTrace struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	FinalRank   int     `json:"final_rank"` // 0 when not in the returned list
	KeywordRank int     `json:"keyword_rank"`
	Keyword     float64 `json:"keyword"`
	KeywordRaw  float64 `json:"keyword_raw"`
	VectorRank  int     `json:"vector_rank"`
	Cosine      float64 `json:"cosine"`
	EntityRank  int     `json:"entity_rank"`
	Entity      float64 `json:"entity"`
	Relevance   float64 `json:"relevance"`
	Score       float64 `json:"score"`
}

type rankedTrace struct {
	Rank      int     `json:"rank"`
	ID        string  `json:"id"`
	Source    string  `json:"source"`
	Relevant  bool    `json:"relevant"`
	Content   string  `json:"content"`
	Keyword   float64 `json:"keyword"`
	Cosine    float64 `json:"cosine"`
	Entity    float64 `json:"entity"`
	Relevance float64 `json:"relevance"`
	Score     float64 `json:"score"`
}

func buildTrace(q evalset.Question, tr *retrieval.Trace, docByID map[uuid.UUID]evalset.Doc, present map[string]bool) questionTrace {
	qt := questionTrace{
		ID: q.ID, Category: q.Category, Question: q.Question, Answer: q.Answer,
		Keywords: tr.Keywords, Unsearchable: tr.Unsearchable, QueryEntities: tr.QueryEntities,
		PoolSizes: map[string]int{"keyword": len(tr.Keyword), "vector": len(tr.Vector), "entity": len(tr.Entity), "ranked": len(tr.Ranked)},
		StageMs:   make(map[string]float64, len(tr.Stages)),
	}
	for name, d := range tr.Stages {
		qt.StageMs[name] = float64(d.Microseconds()) / 1000
	}
	rankIn := func(pool []retrieval.Candidate, id uuid.UUID) (int, retrieval.Candidate) {
		for i, c := range pool {
			if c.ID == id {
				return i + 1, c
			}
		}
		return 0, retrieval.Candidate{}
	}
	finalRank := make(map[uuid.UUID]int, len(tr.Ranked))
	ranked := make(map[uuid.UUID]retrieval.Ranked, len(tr.Ranked))
	for i, r := range tr.Ranked {
		finalRank[r.ID] = i + 1
		ranked[r.ID] = r
	}

	truth := make(map[string]bool, len(q.Evidence))
	for _, dia := range q.Evidence {
		key := evalset.TurnKey(q.Conversation, dia)
		truth[key] = true
		et := evidenceTrace{Key: key, Present: present[key]}
		for id, d := range docByID {
			for _, src := range d.Sources {
				if src != key {
					continue
				}
				dt := docTrace{ID: id.String(), Content: d.Content, FinalRank: finalRank[id]}
				var c retrieval.Candidate
				dt.KeywordRank, c = rankIn(tr.Keyword, id)
				dt.Keyword, dt.KeywordRaw = c.Score, c.Raw
				dt.VectorRank, c = rankIn(tr.Vector, id)
				dt.Cosine = c.Score
				dt.EntityRank, c = rankIn(tr.Entity, id)
				dt.Entity = c.Score
				if r, ok := ranked[id]; ok {
					dt.Relevance, dt.Score, dt.Entity = r.Relevance, r.Score, r.Entity
				}
				et.Docs = append(et.Docs, dt)
			}
		}
		sort.Slice(et.Docs, func(i, j int) bool { return et.Docs[i].ID < et.Docs[j].ID })
		qt.Evidence = append(qt.Evidence, et)
	}

	for i, r := range tr.Ranked {
		if i >= 10 {
			break
		}
		d := docByID[r.ID]
		relevant := false
		for _, src := range d.Sources {
			if truth[src] {
				relevant = true
			}
		}
		qt.Top = append(qt.Top, rankedTrace{
			Rank: i + 1, ID: r.ID.String(), Source: strings.Join(d.Sources, " "), Relevant: relevant,
			Content: d.Content, Keyword: r.Keyword, Cosine: r.Cosine, Entity: r.Entity,
			Relevance: r.Relevance, Score: r.Score,
		})
	}
	return qt
}

// The marker table records which corpus a database holds. Outside the graph
// and outside public.memory_embeddings, so it never appears in the index
// sizes or the node counts the eval reports.
const markerTable = "kora_eval.corpus"

func corpusMarker(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS kora_eval"); err != nil {
		return "", fmt.Errorf("eval schema: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+markerTable+" (marker text NOT NULL)"); err != nil {
		return "", fmt.Errorf("eval marker table: %w", err)
	}
	var marker string
	err := pool.QueryRow(ctx, "SELECT marker FROM "+markerTable+" LIMIT 1").Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return marker, err
}

func setCorpusMarker(ctx context.Context, pool *pgxpool.Pool, marker string) error {
	_, err := pool.Exec(ctx, "INSERT INTO "+markerTable+" (marker) VALUES ($1)", marker)
	return err
}

func indexSizes(ctx context.Context, pool *pgxpool.Pool, into map[string]int64) error {
	rows, err := pool.Query(ctx, `
		SELECT n.nspname || '.' || c.relname, pg_relation_size(c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('context0', 'public') AND c.relkind IN ('r', 'i')
		  AND c.relname NOT LIKE '\_ag\_%' ESCAPE '\'`)
	if err != nil {
		return fmt.Errorf("index sizes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return err
		}
		into[name] = size
	}
	return rows.Err()
}

func percentile(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return float64(sorted[i].Microseconds()) / 1000
}

func printReport(w io.Writer, rep Report, ks []int) {
	fmt.Fprintf(w, "corpus=%s embedder=%s graph_signals=%s fusion=%s top_k=%d questions=%d commit=%s\n",
		rep.Corpus, rep.Embedder, rep.GraphSignals, rep.Fusion, rep.TopK, rep.Dataset.Questions, rep.Commit)
	fmt.Fprintf(w, "%-16s %4s %5s", "category", "n", "n/a")
	for _, k := range ks {
		fmt.Fprintf(w, "  hit@%-3d", k)
	}
	fmt.Fprintf(w, "  rec@10  full@10  MRR@10  nDCG@10\n")
	order := append(append([]string{}, evalset.Categories...), "answerable", "all")
	for _, name := range order {
		a, ok := rep.Metrics[name]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "%-16s %4d %5d", name, a.N, a.Unscorable)
		for _, k := range ks {
			fmt.Fprintf(w, "  %.3f  ", a.Hit[k])
		}
		fmt.Fprintf(w, "  %.3f   %.3f    %.3f   %.3f\n", a.Recall[10], a.Full[10], a.MRR, a.NDCG)
	}
	fmt.Fprintf(w, "latency over %d queries: p50 %.1fms  p95 %.1fms  p99 %.1fms  mean %.1fms  max %.1fms\n",
		rep.Latency.Queries, rep.Latency.P50, rep.Latency.P95, rep.Latency.P99, rep.Latency.Mean, rep.Latency.Max)
	fmt.Fprintf(w, "allocs per query: %.0f mallocs, %.0f bytes; heap in use %d bytes\n",
		rep.Allocs.MallocsPerQuery, rep.Allocs.BytesPerQuery, rep.Allocs.HeapInuse)
	stages := make([]string, 0, len(rep.Stages))
	for name := range rep.Stages {
		stages = append(stages, name)
	}
	sort.Strings(stages)
	fmt.Fprintf(w, "stage means (scoring pass):")
	for _, name := range stages {
		fmt.Fprintf(w, "  %s %.1fms", name, rep.Stages[name])
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "load: %d memories, %d entity links, %dms\n", rep.Load.Memories, rep.Load.Entities, rep.Load.Ms)
	names := make([]string, 0, len(rep.Index))
	for n := range rep.Index {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-45s %8.1f KB\n", n, float64(rep.Index[n])/1024)
	}
	fmt.Fprintf(w, "digest %s\n", rep.Digest)
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
