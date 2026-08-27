package extraction

import (
	"strings"
	"testing"
)

// The linking case from the issue: `Caroline` to her dog `Biscuit` to
// `thunderstorms`. Multi-hop questions are the weakest LoCoMo category at 65%
// because the graph has nothing like this to traverse.
func TestExtractEntities_NamesThePeoplePlacesAndWorksAMemoryIsAbout(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "person and pet",
			content: "Caroline adopted a rescue dog named Biscuit.",
			want:    []string{"Caroline", "Biscuit"},
		},
		{
			name:    "place",
			content: "Caroline moved from Sweden to New York in 2019.",
			want:    []string{"Caroline", "Sweden", "New York"},
		},
		{
			name:    "organisation with a particle",
			content: "Melanie works at the Bank of England.",
			want:    []string{"Melanie", "Bank of England"},
		},
		{
			name:    "a quoted title",
			content: `Caroline read "Charlotte's Web" as a child.`,
			want:    []string{"Charlotte's Web", "Caroline"},
		},
		{
			name:    "two people in one memory",
			content: "Melanie gave Caroline a book about beekeeping.",
			want:    []string{"Melanie", "Caroline"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEntities(tt.content)
			for _, want := range tt.want {
				found := false
				for _, g := range got {
					if NormalizeEntity(g) == NormalizeEntity(want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("ExtractEntities(%q) = %v, missing %q", tt.content, got, want)
				}
			}
		})
	}
}

// The direction that actually decides whether this is worth having.
//
// Entity quality bounds everything downstream: over-generating builds a dense
// graph carrying no information, which is exactly the failure a2f53ac hit with
// semantic linking. Without part-of-speech tags every sentence-initial word
// looks like a name, so these rejections carry most of the weight.
func TestExtractEntities_RejectsWhatWouldBuildHubsRatherThanLinks(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		mustReject []string
	}{
		{
			// Capitalised only because it starts a sentence.
			name:       "sentence openers are grammar, not names",
			content:    "The deadline is in April. When it passes, the team files.",
			mustReject: []string{"The", "When"},
		},
		{
			// Mem0 explicitly discards DATE and TIME from spaCy's output for
			// this reason: every memory mentioning Monday would link to every
			// other one.
			name:       "days and months are not entities",
			content:    "Caroline runs every Monday and files taxes in April.",
			mustReject: []string{"Monday", "April"},
		},
		{
			name:       "quantities and ordinals are not entities",
			content:    "Caroline ran 10 km on her Third attempt, twice.",
			mustReject: []string{"10", "Third", "10 km"},
		},
		{
			name:       "seasons and parts of the day are not entities",
			content:    "Caroline visited in Spring and left on Tuesday Morning.",
			mustReject: []string{"Spring", "Morning", "Tuesday"},
		},
		{
			name:       "a bare pronoun is not an entity",
			content:    "She adopted a dog. They named it Biscuit.",
			mustReject: []string{"She", "They"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEntities(tt.content)
			for _, bad := range tt.mustReject {
				for _, g := range got {
					if NormalizeEntity(g) == NormalizeEntity(bad) {
						t.Errorf("ExtractEntities(%q) returned %q; linking every memory "+
							"that mentions it produces a hub carrying no information "+
							"(got %v)", tt.content, bad, got)
					}
				}
			}
		})
	}
}

// A name at the start of a sentence is the ambiguous case, and the common one:
// the extraction prompt demands "Caroline adopted a dog", never "she adopted a
// dog", so most memories open with a name.
func TestExtractEntities_KeepsANameThatOpensASentence(t *testing.T) {
	got := ExtractEntities("Caroline adopted a rescue dog. Caroline walks him twice a day.")

	found := false
	for _, g := range got {
		if NormalizeEntity(g) == "caroline" {
			found = true
		}
	}
	if !found {
		t.Errorf("ExtractEntities = %v, missing Caroline: the extraction prompt "+
			"demands memories name the person, so almost every memory opens with one", got)
	}
}

// The same entity mentioned in two memories has to resolve to one node, which
// is what makes the second hop possible at all.
func TestNormalizeEntity_ResolvesMentionsOfOneEntityToOneKey(t *testing.T) {
	same := [][]string{
		{"Biscuit", "biscuit", "BISCUIT", "Biscuit's", " Biscuit ", "Biscuit."},
		{"New York", "new york", "New  York"},
		{"Charlotte's Web", "charlotte's web"},
	}
	for _, group := range same {
		base := NormalizeEntity(group[0])
		for _, variant := range group[1:] {
			if got := NormalizeEntity(variant); got != base {
				t.Errorf("NormalizeEntity(%q) = %q, want %q: these are one entity "+
					"and must resolve to one node", variant, got, base)
			}
		}
	}

	// And the other direction: different names must not collide, or unrelated
	// memories become reachable from each other.
	different := []string{"Baker", "Bakers", "Caroline", "Carolina", "Melanie"}
	seen := make(map[string]string)
	for _, name := range different {
		key := NormalizeEntity(name)
		if prev, ok := seen[key]; ok {
			t.Errorf("%q and %q both normalise to %q; unrelated memories would "+
				"become reachable from each other", prev, name, key)
		}
		seen[key] = name
	}
}

// Every entity becomes a node and an edge, so an over-generating memory grows
// the graph with memories x entities. The cap is the same arithmetic that
// bounds autoLinkByTags and detectAndSupersede.
func TestExtractEntities_IsBounded(t *testing.T) {
	var b strings.Builder
	for _, n := range []string{
		"Caroline", "Melanie", "Biscuit", "Pepper", "Marlow", "Juniper",
		"Tobias", "Winnie", "Clementine", "Hollis", "Sweden", "Lisbon",
	} {
		b.WriteString("met " + n + " and ")
	}

	got := ExtractEntities(b.String())
	if len(got) > maxEntitiesPerMemory {
		t.Errorf("ExtractEntities returned %d entities (%v); the cap is %d, because "+
			"every entity is a node and an edge", len(got), got, maxEntitiesPerMemory)
	}
}

// Empty and degenerate input must produce nothing rather than a node named "".
func TestExtractEntities_ProducesNothingForContentWithNoNames(t *testing.T) {
	for _, content := range []string{
		"",
		"   ",
		"the deadline is in the spring",
		"!!! ??? ...",
		"a b c",
	} {
		if got := ExtractEntities(content); len(got) > 0 {
			t.Errorf("ExtractEntities(%q) = %v, want none", content, got)
		}
	}

	if NormalizeEntity("   ") != "" {
		t.Error("whitespace normalised to a non-empty key")
	}
}
