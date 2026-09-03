package evalset

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestParseLoCoMoDate pins the adapter's date parsing, including the two
// am/pm edge cases (12 am is midnight, 12 pm is noon) that a naive "add 12 for
// pm" rule gets wrong.
func TestParseLoCoMoDate(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantTime      time.Time
		wantFormatted string
	}{
		{
			name:          "afternoon",
			input:         "1:56 pm on 8 May, 2023",
			wantTime:      time.Date(2023, time.May, 8, 13, 56, 0, 0, time.UTC),
			wantFormatted: "1:56 pm on 8 May, 2023",
		},
		{
			// 12 am is hour 0, not hour 12: the naive "pm adds 12" rule
			// leaves am alone, which is correct everywhere except here.
			name:          "12 am is midnight",
			input:         "12:00 am on 1 January, 2023",
			wantTime:      time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC),
			wantFormatted: "12:00 am on 1 January, 2023",
		},
		{
			// 12 pm must stay hour 12, not become 24: the naive rule adds
			// 12 to every pm hour except this one.
			name:          "12 pm is noon",
			input:         "12:30 pm on 15 June, 2023",
			wantTime:      time.Date(2023, time.June, 15, 12, 30, 0, 0, time.UTC),
			wantFormatted: "12:30 pm on 15 June, 2023",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, formatted, ok := ParseLoCoMoDate(tt.input)
			if !ok {
				t.Fatalf("ParseLoCoMoDate(%q) ok = false, want true", tt.input)
			}
			if !got.Equal(tt.wantTime) {
				t.Errorf("ParseLoCoMoDate(%q) time = %v, want %v", tt.input, got, tt.wantTime)
			}
			// The formatted string must round-trip: it is what the corpus
			// stores, and a drift here means the eval scores different text
			// than the benchmark ingested.
			if formatted != tt.wantFormatted {
				t.Errorf("ParseLoCoMoDate(%q) formatted = %q, want %q", tt.input, formatted, tt.wantFormatted)
			}
		})
	}
}

func TestParseLoCoMoDate_Unparseable(t *testing.T) {
	got, formatted, ok := ParseLoCoMoDate("sometime last week")
	if ok {
		t.Fatalf("ParseLoCoMoDate(unparseable) ok = true, want false (got time=%v formatted=%q)", got, formatted)
	}
}

// TestRenderTurn pins the adapter's renderTranscript for one utterance:
// grammatical attribution, an optional date prefix, inner newlines collapsed
// to a single space, and an empty utterance rendering to nothing so the
// caller can drop it before ingestion.
func TestRenderTurn(t *testing.T) {
	tests := []struct {
		name          string
		formattedDate string
		speaker       string
		text          string
		want          string
	}{
		{
			name:          "date present",
			formattedDate: "1:56 pm on 8 May, 2023",
			speaker:       "Alice",
			text:          "I moved to Paris.",
			want:          "On 1:56 pm on 8 May, 2023, Alice said that I moved to Paris.",
		},
		{
			name:          "date absent",
			formattedDate: "",
			speaker:       "Bob",
			text:          "I like tea.",
			want:          "Bob said that I like tea.",
		},
		{
			name:          "inner newlines collapsed to one space",
			formattedDate: "",
			speaker:       "Dana",
			text:          "First line\n\nSecond line\nThird line",
			want:          "Dana said that First line Second line Third line",
		},
		{
			name:          "empty text renders empty string",
			formattedDate: "1:56 pm on 8 May, 2023",
			speaker:       "Eve",
			text:          "",
			want:          "",
		},
		{
			name:          "whitespace-only text renders empty string",
			formattedDate: "",
			speaker:       "Frank",
			text:          "   \n  ",
			want:          "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderTurn(tt.formattedDate, tt.speaker, tt.text)
			if got != tt.want {
				t.Errorf("RenderTurn(%q, %q, %q) = %q, want %q", tt.formattedDate, tt.speaker, tt.text, got, tt.want)
			}
		})
	}
}

// TestParseEvidence pins the three ways LoCoMo's evidence lists deviate from
// one "D<session>:<turn>" string per entry, which is why the parser tokenizes
// and normalises rather than treating each entry as a ready-made id.
func TestParseEvidence(t *testing.T) {
	tests := []struct {
		name string
		raw  []string
		want []string
	}{
		{
			name: "normal ids",
			raw:  []string{"D1:1", "D2:3"},
			want: []string{"D1:1", "D2:3"},
		},
		{
			name: "multiple ids packed into one string",
			raw:  []string{"D22:1 D22:2 D9:10 D9:11"},
			want: []string{"D22:1", "D22:2", "D9:10", "D9:11"},
		},
		{
			name: "zero-padded turn number normalises",
			raw:  []string{"D30:05"},
			want: []string{"D30:5"},
		},
		{
			name: "duplicates removed",
			raw:  []string{"D1:1", "D1:1", "D1:1"},
			want: []string{"D1:1"},
		},
		{
			name: "garbage tokens ignored",
			raw:  []string{"not-an-id", "D5:5", "???", ""},
			want: []string{"D5:5"},
		},
		{
			name: "empty input",
			raw:  nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEvidence(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseEvidence(%v) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// locomoFixture is a tiny locomo10.json-shaped file: one conversation, two
// sessions (one dated, one not), and three questions covering a string
// answer, a numeric answer, and an adversarial question.
const locomoFixture = `[
  {
    "sample_id": "conv-1",
    "qa": [
      {"question": "Where does Alice live?", "answer": "Paris", "evidence": ["D1:1"], "category": 1},
      {"question": "How many pets does Bob have?", "answer": 3, "evidence": ["D1:2"], "category": 2},
      {"question": "Adversarial question", "evidence": ["D2:1"], "category": 5, "adversarial_answer": "Not answerable"}
    ],
    "conversation": {
      "session_1_date_time": "1:56 pm on 8 May, 2023",
      "session_1": [
        {"speaker": "Alice", "dia_id": "D1:1", "text": "I live in Paris."},
        {"speaker": "Bob", "dia_id": "D1:2", "text": "I have 3 pets."}
      ],
      "session_2": [
        {"speaker": "Alice", "dia_id": "D2:1", "text": "No date on this session."}
      ]
    }
  }
]`

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "locomo.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeFixture(t, locomoFixture)

	ds, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture back: %v", err)
	}
	sum := sha256.Sum256(raw)
	wantSHA := hex.EncodeToString(sum[:])
	if ds.SHA256 != wantSHA {
		t.Errorf("SHA256 = %q, want %q (sha256 of the file's own bytes)", ds.SHA256, wantSHA)
	}
	if len(ds.SHA256) != 64 {
		t.Errorf("SHA256 has %d hex chars, want 64", len(ds.SHA256))
	}

	wantTurns := []struct {
		diaID, speaker, content string
		session                 int
		dated                   bool
	}{
		{"D1:1", "Alice", "On 1:56 pm on 8 May, 2023, Alice said that I live in Paris.", 1, true},
		{"D1:2", "Bob", "On 1:56 pm on 8 May, 2023, Bob said that I have 3 pets.", 1, true},
		{"D2:1", "Alice", "Alice said that No date on this session.", 2, false},
	}
	if len(ds.Turns) != len(wantTurns) {
		t.Fatalf("got %d turns, want %d: %+v", len(ds.Turns), len(wantTurns), ds.Turns)
	}
	for i, want := range wantTurns {
		got := ds.Turns[i]
		if got.DiaID != want.diaID || got.Speaker != want.speaker || got.Content != want.content || got.Session != want.session {
			t.Errorf("turn %d = %+v, want dia=%s speaker=%s session=%d content=%q", i, got, want.diaID, want.speaker, want.session, want.content)
		}
		if got.Conversation != "conv-1" {
			t.Errorf("turn %d conversation = %q, want conv-1", i, got.Conversation)
		}
		if want.dated && got.Date.IsZero() {
			t.Errorf("turn %d (%s) has zero Date, want the session's timestamp", i, want.diaID)
		}
		if !want.dated && !got.Date.IsZero() {
			t.Errorf("turn %d (%s) has Date %v, want zero: this session carried no date_time", i, want.diaID, got.Date)
		}
	}

	if len(ds.Questions) != 3 {
		t.Fatalf("got %d questions, want 3: %+v", len(ds.Questions), ds.Questions)
	}
	q0, q1, q2 := ds.Questions[0], ds.Questions[1], ds.Questions[2]

	// Code 1 is multi-hop and code 2 is temporal: see categoryNames for why
	// the codes do not follow the paper's listing order.
	if q0.ID != "conv-1-q0" || q0.Category != "multi-hop" || q0.Answer != "Paris" {
		t.Errorf("q0 = %+v, want id=conv-1-q0 category=multi-hop answer=Paris", q0)
	}
	if !reflect.DeepEqual(q0.Evidence, []string{"D1:1"}) {
		t.Errorf("q0.Evidence = %v, want [D1:1]", q0.Evidence)
	}

	// LoCoMo's answer field is a JSON number here; the adapter stringifies it
	// exactly as JavaScript's String() would.
	if q1.ID != "conv-1-q1" || q1.Category != "temporal" || q1.Answer != "3" {
		t.Errorf("q1 = %+v, want id=conv-1-q1 category=temporal answer=3", q1)
	}

	if q2.ID != "conv-1-q2" || q2.Category != "adversarial" || q2.AdversarialAnswer != "Not answerable" {
		t.Errorf("q2 = %+v, want id=conv-1-q2 category=adversarial adversarial_answer=Not answerable", q2)
	}
	if !reflect.DeepEqual(q2.Evidence, []string{"D2:1"}) {
		t.Errorf("q2.Evidence = %v, want [D2:1]", q2.Evidence)
	}
}

func TestLoad_Pinned(t *testing.T) {
	path := writeFixture(t, locomoFixture)

	ds, err := Load(path, []string{"conv-1-q2", "conv-1-q0"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ds.Questions) != 2 {
		t.Fatalf("got %d questions, want 2: %+v", len(ds.Questions), ds.Questions)
	}
	// Pinned order, not dataset order.
	if ds.Questions[0].ID != "conv-1-q2" || ds.Questions[1].ID != "conv-1-q0" {
		t.Errorf("got order [%s %s], want [conv-1-q2 conv-1-q0]", ds.Questions[0].ID, ds.Questions[1].ID)
	}
}

func TestLoad_UnknownPinnedID(t *testing.T) {
	path := writeFixture(t, locomoFixture)

	_, err := Load(path, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("Load with an unknown pinned id returned nil error, want an error naming it")
	}
}
