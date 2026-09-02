// Package evalset is the deterministic half of the offline retrieval
// evaluation behind `make eval`.
//
// It builds the LoCoMo questions and turns exactly as the benchmark adapter
// rendered them, reads and writes the frozen embedding fixture, turns either
// the rendered turns or a snapshot of extracted memories into a corpus with
// ground-truth labels, and scores ranked results against those labels.
// cmd/eval drives it against a real database.
//
// Nothing in this package talks to a network or a model. The one exception,
// building the embedding fixture, lives in cmd/eval and is run once.
//
// # Why this exists
//
// Every number Kora had published came from the memorybench harness, where
// the answer, the judge's verdict and even the retrieval metrics are produced
// by a language model over an API. Those numbers are not reproducible offline,
// and a benchmark that cannot be re-run cannot arbitrate a change. This
// package scores retrieval against LoCoMo's own evidence annotations -- the
// dialogue turns each question was written from -- which is fixed data.
package evalset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DatasetURL is where memorybench fetches the dataset from. The file is
// CC BY-NC 4.0 (snap-research/locomo, LICENSE.txt), so it is downloaded once
// into a gitignored directory rather than committed beside Apache-2.0 code.
const DatasetURL = "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json"

// Turn is one utterance of a LoCoMo conversation.
type Turn struct {
	// DiaID is LoCoMo's identifier, "D<session>:<turn>". Evidence annotations
	// refer to these, so they are the unit of ground truth.
	DiaID        string
	Conversation string
	Session      int
	// Date is the session's timestamp; zero when the dataset carried none.
	Date    time.Time
	Speaker string
	Text    string
	// Content is the turn as the benchmark adapter renders it before
	// ingestion: "On <date>, <speaker> said that <text>". The corpus stores
	// this, so the evaluation sees exactly what the benchmark did.
	Content string
}

// Question is one benchmark question with its ground truth.
type Question struct {
	ID           string
	Conversation string
	// Category is one of single-hop, multi-hop, temporal, world-knowledge,
	// adversarial. Adversarial questions have no answer in the corpus; their
	// evidence names the turn the question was built to resemble.
	Category          string
	Question          string
	Answer            string
	AdversarialAnswer string
	// Evidence is the normalised list of DiaIDs the answer rests on.
	Evidence []string
}

// Dataset is the parsed benchmark: every turn, and the questions under
// evaluation.
type Dataset struct {
	Turns     []Turn
	Questions []Question
	// SHA256 of the file the dataset was read from, so a run can state which
	// bytes it evaluated against.
	SHA256 string
}

// categoryNames maps LoCoMo's integer category codes to names.
//
// The codes do NOT follow the order the paper lists the categories in, and
// the memorybench harness had them wrong: it read 1 as single-hop, 2 as
// multi-hop, 3 as temporal and 4 as world-knowledge. The dataset's own counts
// settle it (code 4 has 841 questions, and the paper says single-hop is the
// largest category at 841; code 3 has 96, the paper's open-domain count),
// and snap-research/locomo issue #29 and Mem0's harness agree. Every
// per-category number in docs/research/ before 2026-09-02 carries the
// harness's labels; the correction table is in docs/OPTIMIZATION_REPORT.md.
var categoryNames = map[int]string{
	1: "multi-hop",
	2: "temporal",
	3: "open-domain",
	4: "single-hop",
	5: "adversarial",
}

// Categories lists the question categories in reporting order.
var Categories = []string{"single-hop", "multi-hop", "temporal", "open-domain", "adversarial"}

type rawMessage struct {
	Speaker string `json:"speaker"`
	DiaID   string `json:"dia_id"`
	Text    string `json:"text"`
}

type rawQA struct {
	Question          string          `json:"question"`
	Answer            json.RawMessage `json:"answer"`
	Evidence          []string        `json:"evidence"`
	Category          int             `json:"category"`
	AdversarialAnswer string          `json:"adversarial_answer"`
}

type rawItem struct {
	SampleID     string                     `json:"sample_id"`
	QA           []rawQA                    `json:"qa"`
	Conversation map[string]json.RawMessage `json:"conversation"`
}

// LoadPinned reads a JSON list of question ids.
func LoadPinned(path string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return ids, nil
}

// Load parses locomo10.json. When pinned is non-empty only those questions
// are kept, in the pinned order; otherwise every question is kept in dataset
// order.
func Load(path string, pinned []string) (*Dataset, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	raw, err := io.ReadAll(io.TeeReader(f, h))
	if err != nil {
		return nil, err
	}

	var items []rawItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	ds := &Dataset{SHA256: hex.EncodeToString(h.Sum(nil))}
	for _, item := range items {
		if err := ds.addConversation(item); err != nil {
			return nil, err
		}
	}

	if len(pinned) > 0 {
		byID := make(map[string]Question, len(ds.Questions))
		for _, q := range ds.Questions {
			byID[q.ID] = q
		}
		kept := make([]Question, 0, len(pinned))
		for _, id := range pinned {
			q, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("pinned question %q is not in the dataset", id)
			}
			kept = append(kept, q)
		}
		ds.Questions = kept
	}
	return ds, nil
}

func (ds *Dataset) addConversation(item rawItem) error {
	for i := 1; ; i++ {
		key := "session_" + strconv.Itoa(i)
		body, ok := item.Conversation[key]
		if !ok {
			break
		}
		var msgs []rawMessage
		if err := json.Unmarshal(body, &msgs); err != nil {
			// The adapter skips a session that is not an array; mirror it.
			continue
		}

		var date time.Time
		var formatted string
		if rawDate, ok := item.Conversation[key+"_date_time"]; ok {
			var s string
			if err := json.Unmarshal(rawDate, &s); err == nil {
				date, formatted, _ = ParseLoCoMoDate(s)
			}
		}

		for _, m := range msgs {
			content := RenderTurn(formatted, m.Speaker, m.Text)
			if content == "" {
				// The adapter drops an empty utterance before ingestion.
				continue
			}
			ds.Turns = append(ds.Turns, Turn{
				DiaID:        m.DiaID,
				Conversation: item.SampleID,
				Session:      i,
				Date:         date,
				Speaker:      m.Speaker,
				Text:         m.Text,
				Content:      content,
			})
		}
	}

	for i, qa := range item.QA {
		category, ok := categoryNames[qa.Category]
		if !ok {
			return fmt.Errorf("%s-q%d: unknown category %d", item.SampleID, i, qa.Category)
		}
		ds.Questions = append(ds.Questions, Question{
			ID:                fmt.Sprintf("%s-q%d", item.SampleID, i),
			Conversation:      item.SampleID,
			Category:          category,
			Question:          qa.Question,
			Answer:            answerString(qa.Answer),
			AdversarialAnswer: qa.AdversarialAnswer,
			Evidence:          ParseEvidence(qa.Evidence),
		})
	}
	return nil
}

// answerString renders the answer field, which LoCoMo stores as either a
// string or a number, the way the adapter does: String(qa.answer).
func answerString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

var locomoDate = regexp.MustCompile(`(?i)(\d+):(\d+)\s*(am|pm)\s*on\s*(\d+)\s*(\w+),?\s*(\d+)`)

var monthNames = []string{"january", "february", "march", "april", "may", "june",
	"july", "august", "september", "october", "november", "december"}

// ParseLoCoMoDate is the adapter's parseLocomoDate: it reads "1:56 pm on 8
// May, 2023" and returns the instant and the string the adapter prefixes
// every utterance with.
//
// The formatted string is rebuilt rather than passed through so that the two
// implementations cannot drift: what the eval stores must be what the
// benchmark stored, or the evaluation is of a different corpus.
func ParseLoCoMoDate(s string) (time.Time, string, bool) {
	m := locomoDate.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, "", false
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	ampm := strings.ToLower(m[3])
	day, _ := strconv.Atoi(m[4])
	year, _ := strconv.Atoi(m[6])

	if ampm == "pm" && hour != 12 {
		hour += 12
	}
	if ampm == "am" && hour == 12 {
		hour = 0
	}
	month := -1
	for i, name := range monthNames {
		if strings.HasPrefix(name, strings.ToLower(m[5])) {
			month = i
			break
		}
	}
	if month < 0 {
		return time.Time{}, "", false
	}

	t := time.Date(year, time.Month(month+1), day, hour, minute, 0, 0, time.UTC)
	display := hour % 12
	if display == 0 {
		display = 12
	}
	suffix := "am"
	if hour >= 12 {
		suffix = "pm"
	}
	// m[2], m[4] and m[5] are the original captures, as in the adapter: the
	// minute keeps its zero padding and the month keeps its case.
	formatted := fmt.Sprintf("%d:%s %s on %s %s, %d", display, m[2], suffix, m[4], m[5], year)
	return t, formatted, true
}

var innerNewlines = regexp.MustCompile(`\s*\n\s*`)

// RenderTurn is the adapter's renderTranscript for one utterance. The
// attribution is grammatical ("said that") rather than a "Name:" label,
// because the rule extractor strips a label and the memory would lose who
// spoke.
func RenderTurn(formattedDate, speaker, text string) string {
	content := strings.TrimSpace(innerNewlines.ReplaceAllString(text, " "))
	if content == "" {
		return ""
	}
	if formattedDate != "" {
		return fmt.Sprintf("On %s, %s said that %s", formattedDate, speaker, content)
	}
	return fmt.Sprintf("%s said that %s", speaker, content)
}

var diaID = regexp.MustCompile(`^D(\d+):(\d+)$`)

// ParseEvidence normalises LoCoMo's evidence list. Three of the pinned 200
// deviate from one "D<s>:<t>" per entry: two hold several ids in one string
// and one is zero-padded ("D30:05"). Both are read rather than dropped, since
// dropping them would score those questions as having no ground truth.
func ParseEvidence(raw []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, item := range raw {
		for _, tok := range strings.Fields(item) {
			m := diaID.FindStringSubmatch(tok)
			if m == nil {
				continue
			}
			s, _ := strconv.Atoi(m[1])
			t, _ := strconv.Atoi(m[2])
			id := fmt.Sprintf("D%d:%d", s, t)
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// TurnKey names a turn uniquely across conversations: dia ids restart at
// D1:1 in every conversation.
func TurnKey(conversation, diaID string) string {
	return conversation + "/" + diaID
}

// SortQuestions orders questions by id, for stable output when no pinned
// order was supplied.
func SortQuestions(qs []Question) {
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].ID < qs[j].ID })
}
