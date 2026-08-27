package extraction

import "testing"

// The corpus this was built against held 6,010 memories for 573 distinct
// facts. `Caroline is transgender.` was stored 25 times and
// `Caroline is a transgender person.` another 16 times as separate rows, so
// the two cases below are the exact ones the redundancy test has to catch.
func TestSubsumes_CatchesTheRedundancyMeasuredInTheCorpus(t *testing.T) {
	tests := []struct {
		name       string
		newer, old string
		want       Subsumption
	}{
		{
			name:  "identical text",
			newer: "Caroline is transgender.",
			old:   "Caroline is transgender.",
			want:  Equivalent,
		},
		{
			name:  "same fact, different wording, longer arrives second",
			newer: "Caroline is a transgender person.",
			old:   "Caroline is transgender.",
			want:  NewSubsumesOld,
		},
		{
			name:  "same fact, different wording, shorter arrives second",
			newer: "Caroline is transgender.",
			old:   "Caroline is a transgender person.",
			want:  OldSubsumesNew,
		},
		{
			name:  "case and punctuation are not content",
			newer: "caroline is transgender",
			old:   "Caroline is TRANSGENDER!",
			want:  Equivalent,
		},
		{
			name:  "the longer memory adds a specific",
			newer: "Caroline adopted a rescue dog named Biscuit.",
			old:   "Caroline adopted a rescue dog.",
			want:  NewSubsumesOld,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Subsumes(tt.newer, tt.old); got != tt.want {
				t.Errorf("Subsumes(%q, %q) = %v, want %v", tt.newer, tt.old, got, tt.want)
			}
		})
	}
}

// The other direction, and the one that decides whether this is safe to run on
// a write path: consolidation must never merge two memories that say different
// things, because the row it drops cannot be recovered.
//
// Every case here is a pair that a bag-of-words or dense embedding scores as
// highly similar, which is why similarity alone cannot be the test.
func TestSubsumes_RefusesToMergeDifferentFacts(t *testing.T) {
	tests := []struct {
		name       string
		newer, old string
	}{
		{
			// A preposition is not a stop word: it carries the fact.
			name:  "opposite prepositions",
			newer: "Caroline moved to Sweden.",
			old:   "Caroline moved from Sweden.",
		},
		{
			// The shorter is a subsequence of the longer once "not" is the
			// only difference, so polarity has to be checked separately.
			name:  "negation",
			newer: "Caroline is not a teacher.",
			old:   "Caroline is a teacher.",
		},
		{
			name:  "negation, arriving in the other order",
			newer: "Caroline is a teacher.",
			old:   "Caroline is not a teacher.",
		},
		{
			// Identical word sets, opposite meanings. This is why the test is
			// an ordered subsequence rather than set containment.
			name:  "swapped subject and object",
			newer: "Melanie gave Caroline a book.",
			old:   "Caroline gave Melanie a book.",
		},
		{
			name:  "different quantities",
			newer: "Caroline ran 10 km on Saturday.",
			old:   "Caroline ran 5 km on Saturday.",
		},
		{
			name:  "different dates",
			newer: "Caroline gave a talk on 9 June 2023.",
			old:   "Caroline gave a talk on 9 July 2023.",
		},
		{
			name:  "unrelated facts",
			newer: "Caroline adopted a rescue dog named Biscuit.",
			old:   "The quarterly tax filing deadline is in April.",
		},
		{
			// The qualifier is the fact. Truth `counseling for Transgender
			// people` was graded wrong when answered as `counseling`, so the
			// two must not collapse into one row either.
			name:  "a dropped qualifier is a different fact, and the shorter one arrives second",
			newer: "Caroline offers counseling.",
			old:   "Caroline offers counseling for transgender people.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Subsumes(tt.newer, tt.old)
			// The qualifier case is a genuine subsequence, so it is allowed to
			// report OldSubsumesNew: the memory that survives is the one
			// carrying the qualifier. What must never happen is the shorter
			// text replacing the longer.
			if got == NewSubsumesOld || got == Equivalent {
				t.Errorf("Subsumes(%q, %q) = %v: consolidating these loses a fact",
					tt.newer, tt.old, got)
			}
		})
	}
}

// A memory with almost no content words must not subsume anything. Without
// this, a one-word memory would be merged into every longer memory mentioning
// that word.
func TestSubsumes_IgnoresMemoriesWithTooLittleContent(t *testing.T) {
	if got := Subsumes("Caroline walks Biscuit twice a day.", "Biscuit."); got != Distinct {
		t.Errorf("a one-token memory reported %v against a longer one; "+
			"it carries too little content for containment to mean anything", got)
	}
	if got := Subsumes("", "Caroline is transgender."); got != Distinct {
		t.Errorf("an empty memory reported %v; it says nothing about anything", got)
	}
}

// FoldRedundant is what stops a single conversation storing the same fact
// three times before it ever reaches the database, which also saves embedding
// the duplicates.
func TestFoldRedundant_KeepsTheMostInformativeWording(t *testing.T) {
	in := []ExtractedMemory{
		{Content: "Caroline is transgender.", Type: "semantic"},
		{Content: "Caroline is a transgender person.", Type: "semantic"},
		{Content: "Caroline is transgender.", Type: "semantic"},
		{Content: "Caroline adopted a rescue dog named Biscuit.", Type: "semantic"},
		{Content: "The quarterly tax filing deadline is in April.", Type: "semantic"},
	}

	got := FoldRedundant(in)

	if len(got) != 3 {
		var contents []string
		for _, m := range got {
			contents = append(contents, m.Content)
		}
		t.Fatalf("FoldRedundant kept %d memories, want 3: %v", len(got), contents)
	}

	// The wording that survives must be the one carrying the most content.
	if got[0].Content != "Caroline is a transgender person." {
		t.Errorf("kept %q; the surviving row must be the more informative wording",
			got[0].Content)
	}
	// Order is otherwise preserved, so the response still reads in the order
	// the conversation stated the facts.
	if got[1].Content != "Caroline adopted a rescue dog named Biscuit." ||
		got[2].Content != "The quarterly tax filing deadline is in April." {
		t.Errorf("distinct memories were reordered or dropped: %v", got)
	}
}

// Tags survive a fold. They are the extractor's only structured output besides
// the type, and dropping them would quietly disable tag-based auto-linking for
// any fact that happened to be stated twice.
func TestFoldRedundant_UnionsTags(t *testing.T) {
	got := FoldRedundant([]ExtractedMemory{
		{Content: "Caroline is transgender.", Type: "semantic", Tags: []string{"identity"}},
		{Content: "Caroline is a transgender person.", Type: "semantic", Tags: []string{"identity", "caroline"}},
	})

	if len(got) != 1 {
		t.Fatalf("kept %d memories, want 1", len(got))
	}
	if len(got[0].Tags) != 2 {
		t.Errorf("tags = %v, want the union of both memories' tags", got[0].Tags)
	}
}

// A different memory type is a different claim about the fact's nature, and
// GetProfile splits static from dynamic on exactly that field. Merging across
// types would silently reclassify a fact.
func TestFoldRedundant_DoesNotMergeAcrossTypes(t *testing.T) {
	got := FoldRedundant([]ExtractedMemory{
		{Content: "Caroline is transgender.", Type: "semantic"},
		{Content: "Caroline is a transgender person.", Type: "episodic"},
	})
	if len(got) != 2 {
		t.Errorf("kept %d memories, want 2: consolidating across types reclassifies a fact", len(got))
	}
}

// Polarity is not only "not".
//
// A subsequence test sees "Caroline denies that she owns a car" as a superset
// of "Caroline owns a car" -- every word, in order, with no negation token
// anywhere. Folding the second into the first stores the opposite of what was
// said. Hedges do the same thing more quietly: an intention is not an event.
func TestSubsumes_TreatsDenialAndHedgingAsPolarity(t *testing.T) {
	tests := []struct {
		name       string
		newer, old string
	}{
		{
			name:  "denial",
			newer: "Caroline denies that she owns a car.",
			old:   "Caroline owns a car.",
		},
		{
			name:  "denial, arriving in the other order",
			newer: "Caroline owns a car.",
			old:   "Caroline denies that she owns a car.",
		},
		{
			name:  "refusal",
			newer: "Caroline refuses to move to Lisbon.",
			old:   "Caroline moves to Lisbon.",
		},
		{
			name:  "hedge: an intention is not the event",
			newer: "Caroline plans to move to Lisbon in autumn.",
			old:   "Caroline moves to Lisbon in autumn.",
		},
		{
			name:  "hedge: a possibility is not the event",
			newer: "Caroline might adopt a rescue dog named Biscuit.",
			old:   "Caroline adopts a rescue dog named Biscuit.",
		},
		{
			name:  "a former role is not the current one",
			newer: "Caroline is a former nurse at the county hospital.",
			old:   "Caroline is a nurse at the county hospital.",
		},
		{
			name:  "stopping is not doing",
			newer: "Caroline stopped running along the river every morning.",
			old:   "Caroline runs along the river every morning.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Subsumes(tt.newer, tt.old); got != Distinct {
				t.Errorf("Subsumes(%q, %q) = %v: consolidating these stores "+
					"something other than what was said", tt.newer, tt.old, got)
			}
		})
	}
}

// The polarity guard must not fire on ordinary sentences, or nothing would
// ever consolidate and the redundancy this exists to remove would stay.
//
// Both memories in each pair carry the same polarity, so the guard is meant to
// step aside and let the subsequence test decide.
func TestSubsumes_PolarityGuardDoesNotBlockOrdinaryConsolidation(t *testing.T) {
	tests := []struct {
		name       string
		newer, old string
		want       Subsumption
	}{
		{
			name:  "two statements of the same denial",
			newer: "Caroline denies that she owns a red car.",
			old:   "Caroline denies that she owns a car.",
			want:  NewSubsumesOld,
		},
		{
			name:  "two statements of the same negative fact",
			newer: "Caroline is not a paediatric nurse at the county hospital.",
			old:   "Caroline is not a nurse at the hospital.",
			want:  NewSubsumesOld,
		},
		{
			// "does" is an auxiliary, not a polarity marker. Listing it would
			// make every present-tense question-derived fact unconsolidatable.
			name:  "an auxiliary is not a negation",
			newer: "Caroline does yoga every morning before work.",
			old:   "Caroline does yoga every morning.",
			want:  NewSubsumesOld,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Subsumes(tt.newer, tt.old); got != tt.want {
				t.Errorf("Subsumes(%q, %q) = %v, want %v: the polarity guard is "+
					"blocking a consolidation it should allow",
					tt.newer, tt.old, got, tt.want)
			}
		})
	}
}
