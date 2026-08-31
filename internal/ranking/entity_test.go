package ranking

import "testing"

// The entity boost has to be large enough to matter and small enough not to
// take over. This is the "large enough" half: among candidates the retrievers
// scored identically, the one about the query's subject should come first.
func TestEntityBoost_BreaksTiesBetweenEqualMatches(t *testing.T) {
	const equal = 0.60

	about := ApplyEntityBoost(equal, EntityOverlap([]string{"biscuit"}, []string{"biscuit"}, nil))
	notAbout := ApplyEntityBoost(equal, EntityOverlap([]string{"pepper"}, []string{"biscuit"}, nil))

	if about <= notAbout {
		t.Errorf("a memory naming the query's entity scored %v against %v for one "+
			"that does not; with the retrievers tied, being about the right "+
			"subject is the only signal left", about, notAbout)
	}
}

// And the "small enough" half, which is the one that can do damage.
//
// Same arithmetic the ranking weights use: relevance carries weight 0.75 in
// the composite score, so a relevance bonus of b can only reorder memories
// whose relevance differs by less than b. Mem0's ENTITY_BOOST_WEIGHT of 0.5
// would put any memory naming the subject above every memory that answers the
// question -- and in a corpus about Caroline, nearly every memory names
// Caroline.
func TestEntityBoost_CannotOverturnARealRelevanceDifference(t *testing.T) {
	// A clearly better answer that happens to name none of the query's
	// entities, against a weak one that names all of them.
	strong := ApplyEntityBoost(0.90, 0)
	weak := ApplyEntityBoost(0.75, 1.0)

	if weak >= strong {
		t.Errorf("a memory with relevance 0.75 and a full entity match scored %v, "+
			"beating a 0.90 match at %v; the boost must break ties, not overturn "+
			"a real difference", weak, strong)
	}

	// Stated as the bound rather than the example, so a change to entityBoost
	// has to be deliberate: the boost may never exceed the smallest relevance
	// gap it is allowed to preserve.
	if entityBoost >= 0.15 {
		t.Errorf("entityBoost is %v; above 0.15 it overturns the relevance gap "+
			"this test pins", entityBoost)
	}
}

// The boost is proportional, so naming two of the query's three entities is
// worth more than naming one and less than naming all three.
func TestEntityOverlap_IsTheShareOfTheQuerysEntitiesNamed(t *testing.T) {
	query := []string{"caroline", "biscuit", "sweden"}

	tests := []struct {
		name   string
		memory []string
		want   float64
	}{
		{"none", []string{"melanie"}, 0},
		{"one of three", []string{"caroline"}, 1.0 / 3.0},
		{"two of three", []string{"caroline", "biscuit"}, 2.0 / 3.0},
		{"all three", []string{"caroline", "biscuit", "sweden"}, 1},
		// Extra entities the query did not name neither help nor hurt: the
		// question is how much of the query is covered, not how much of the
		// memory is spent.
		{"all three plus others", []string{"caroline", "biscuit", "sweden", "lisbon"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EntityOverlap(tt.memory, query, nil); got != tt.want {
				t.Errorf("EntityOverlap(%v, %v) = %v, want %v", tt.memory, query, got, tt.want)
			}
		})
	}
}

// A query naming nothing must give every candidate the same treatment, which
// means no treatment. Returning 1.0 for an empty query would hand every
// candidate the full boost, which is the same as handing it to none of them
// while making the code look like it does something.
func TestEntityOverlap_IsZeroWhenThereIsNothingToMatch(t *testing.T) {
	cases := []struct {
		name          string
		memory, query []string
	}{
		{"no query entities", []string{"caroline"}, nil},
		{"no memory entities", nil, []string{"caroline"}},
		{"neither", nil, nil},
		{"empty strings only", []string{""}, []string{""}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := EntityOverlap(tt.memory, tt.query, nil); got != 0 {
				t.Errorf("EntityOverlap(%v, %v) = %v, want 0", tt.memory, tt.query, got)
			}
		})
	}
}

// A duplicated query entity must not count twice, or a query mentioning one
// name repeatedly would inflate its own denominator and dilute the match.
func TestEntityOverlap_CountsDistinctQueryEntities(t *testing.T) {
	got := EntityOverlap([]string{"caroline"}, []string{"caroline", "caroline", "caroline"}, nil)
	if got != 1 {
		t.Errorf("EntityOverlap = %v, want 1: repeating an entity in the query "+
			"does not make it harder to match", got)
	}
}

// The boost must not push a candidate past the top of the scale, which would
// flatten the ordering among the memories that were already strongest.
func TestApplyEntityBoost_StaysInRange(t *testing.T) {
	if got := ApplyEntityBoost(1.0, 1.0); got != 1.0 {
		t.Errorf("ApplyEntityBoost(1.0, 1.0) = %v, want 1.0", got)
	}
	if got := ApplyEntityBoost(-1, 2); got < 0 || got > 1 {
		t.Errorf("ApplyEntityBoost(-1, 2) = %v, which is outside [0, 1]", got)
	}
	// No entity match must leave relevance exactly as it was, so the signal is
	// additive rather than a rescaling of everything.
	if got := ApplyEntityBoost(0.42, 0); got != 0.42 {
		t.Errorf("ApplyEntityBoost(0.42, 0) = %v, want 0.42 unchanged", got)
	}
}

// EntityRelevance is what decides whether entity retrieval does anything at
// all, so its placement against the lexical scale is the contract.
//
// A memory found only by entity has no lexical score and no cosine score. At
// zero it lands in RelevanceTier's unmatched tier, permanently below every
// memory containing any query word however common, and the recall half of the
// feature is unreachable in any project with more than a handful of keyword
// matches. That was the measured behaviour before this constant existed.
func TestEntityMatch_BeatsAWeakLexicalMatch(t *testing.T) {
	// A memory naming every entity the query names, and nothing else.
	entityOnly := RelevanceTier(EntityRelevance(1.0), 0, true)

	// A memory that happens to contain one of three query keywords in its
	// prose, which LexicalRelevance grades at 0.75/3.
	weakLexical := RelevanceTier(LexicalRelevance("an unrelated sentence about deadlines",
		nil, []string{"deadlines", "biscuit", "thunderstorms"}), 0, true)

	if entityOnly <= weakLexical {
		t.Errorf("a full entity match scored %v against %v for a memory sharing one "+
			"common word; being about the thing asked about is stronger evidence "+
			"than incidentally containing one query term", entityOnly, weakLexical)
	}
}

// The other direction: naming the subject is necessary but not sufficient.
// A memory that actually matches the query must still win, or every query in a
// corpus about one person returns the same memories in the same order.
func TestEntityMatch_LosesToAStrongLexicalMatch(t *testing.T) {
	entityOnly := ApplyEntityBoost(
		RelevanceTier(EntityRelevance(1.0), 0, true), 1.0)

	// A memory containing every query keyword, naming none of its entities.
	strongLexical := ApplyEntityBoost(
		RelevanceTier(LexicalRelevance("deadlines and thunderstorms and biscuit tins",
			nil, []string{"deadlines", "biscuit", "thunderstorms"}), 0, true), 0)

	if entityOnly >= strongLexical {
		t.Errorf("an entity-only match scored %v against %v for a memory matching "+
			"every query term; naming the subject is necessary but not sufficient",
			entityOnly, strongLexical)
	}
}

// Weighted overlap: the discrimination weight is the point of entity IDF.
//
// Measured on the ablation baseline: the unweighted signal pulled generic
// entity matches into adversarial questions and hurt 6-to-1, because naming
// an entity that most of the project's memories name is not evidence. The
// weight makes a ubiquitous entity worth almost nothing and a rare one worth
// almost everything, and both directions are pinned here.
func TestEntityOverlap_WeightsDiscriminateByRarity(t *testing.T) {
	weights := map[string]float64{
		"caroline": 0.1, // mentioned by nearly every memory
		"biscuit":  0.9, // mentioned by three
	}

	ubiquitous := EntityOverlap([]string{"caroline"}, []string{"caroline"}, weights)
	rare := EntityOverlap([]string{"biscuit"}, []string{"biscuit"}, weights)

	if ubiquitous != 0.1 {
		t.Errorf("ubiquitous entity overlap = %v, want its weight 0.1: an entity in "+
			"every memory must carry almost no signal", ubiquitous)
	}
	if rare != 0.9 {
		t.Errorf("rare entity overlap = %v, want its weight 0.9", rare)
	}
	if rare <= ubiquitous {
		t.Error("a rare shared entity must outweigh a ubiquitous one; that ordering is the feature")
	}
}

// Nil weights are the unweighted behaviour, exactly.
//
// This is the degradation contract: when mention stats are unavailable the
// caller passes nil, and retrieval must order candidates precisely as it did
// before weighting existed -- worse ordering, never a changed result set.
func TestEntityOverlap_NilWeightsAreUniform(t *testing.T) {
	memory := []string{"caroline", "biscuit"}
	query := []string{"caroline", "biscuit", "melanie"}

	if got := EntityOverlap(memory, query, nil); got != 2.0/3.0 {
		t.Errorf("nil-weight overlap = %v, want 2/3: nil must mean uniform full weight", got)
	}
}

// An entity absent from the weights map counts fully.
//
// Absence means the store had no mention count for it, and an entity with no
// mentions cannot be matched at all -- so a full-weight default only ever
// affects entities that were matched despite the stats missing them, where
// under-counting real evidence would be the worse error.
func TestEntityOverlap_UnknownEntityKeepsFullWeight(t *testing.T) {
	weights := map[string]float64{"caroline": 0.1}

	got := EntityOverlap([]string{"caroline", "miso"}, []string{"caroline", "miso"}, weights)
	want := (0.1 + 1.0) / 2.0
	if got != want {
		t.Errorf("overlap = %v, want %v: unweighted entity must default to full weight", got, want)
	}
}
