# The design competition

Four agents on four different model families, one shared brief, four complete
independent landing pages. This directory keeps the brief and all four
concepts, because the shipped site uses ideas from three of them and the
reasoning behind that is worth being able to re-read.

Open any concept directly in a browser. Each is a single self-contained HTML
file with no build step.

| File | Concept | Model | Lines |
|---|---|---|---|
| `concept-a.html` | The Living Graph | claude-sonnet-5 | 1,118 |
| `concept-b.html` | The Proof | gpt-5.5 | 1,596 |
| `concept-c.html` | The Substrate | gpt-5.6-sol | 540 |
| `concept-d.html` | The Forgetting | gpt-5.6-terra | 65 (minified CSS) |

`BRIEF.md` is what all four received: identical ground-truth facts, a fixed
copy deck, brand tokens, and an explicit ban on inventing claims the project
cannot back. They differed only in an assigned creative direction, so the
comparison is about design rather than about who guessed the product better.

## How they scored

Judged against the brief's own weighted criteria, with every concept rendered
in a real browser at 1440px and 390px rather than read as source.

| Criterion | A | B | C | D |
|---|---|---|---|---|
| Argument lands in two screens *(heaviest)* | partly | **yes** | **yes** | one screen later |
| Visual craft | good | **excellent** | **excellent** | **excellent** |
| Originality | **highest** | familiar | **distinctive** | **unmatched** |
| Restraint | motion competes | **disciplined** | **near-static** | **disciplined** |
| Mobile as a real design | busy | nav overlap | 15,548px tall | **best** |
| Technical health | canvas bug | **clean** | **clean** | clipped code blocks |

Two concepts arrived with real defects, which were fixed before judging so
none was penalised for a rendering accident rather than its design:

- **A** shipped an invalid canvas gradient (`rgb(133,61,248)b3`) that threw on
  every frame and left the graph blank.
- **D** clipped overflowing code blocks with `overflow-x: hidden` rather than
  fixing the layout underneath.

## What shipped, and why

The user narrowed the scope right after the concepts landed: an overview page
with a waitlist plus releases, blog, and docs, rather than the full product
tour the brief described. So no single concept shipped whole. Three
contributed:

- **D** gave the hero. An agent losing yesterday's context, then the same
  exchange replayed with memory intact, was the most memorable thing any of
  the four produced. Its "Replay with memory intact" control survives almost
  verbatim as the toggle in `HeroDemo.tsx`.
- **D** also gave the editorial voice: a serif display headline, generous
  spacing, sentences rather than feature bullets. The specific face differs -
  D used Georgia, the shipped site uses Instrument Serif with a
  metric-adjusted Georgia fallback - but the decision to set the hero in a
  serif at all came from this concept.
- **B** gave the discipline. Its willingness to make one argument and stop is
  why the shipped home page is four sections instead of nine.
- **C** gave the monospace eyebrow labels and the hairline-rule rhythm that
  separates sections.

**A** shipped nothing. Its full-page force-directed canvas was the most
ambitious idea in the set and the least compatible with a page whose job is to
be read quickly, and its node labels collided with the headline at every width
tested.

## Worth knowing if you run this again

- A shared brief with fixed facts is what made the concepts comparable, and the
  explicit ban on invented claims held: none of the four produced a user count,
  a benchmark figure, or a "trusted by" band, though several had layouts with an
  obvious place for one. Stating the prohibition and the reason for it was
  enough.
- Assigning each agent a distinct creative direction produced far more
  variance than four agents given the same open prompt would have.
- Rendering every concept in a browser mattered more than expected. Two of the
  four had defects invisible in the source, and one of those was fatal to the
  concept's central idea.
- Every concept ran long: 8 to 12 sections against the 4 that shipped. Given a
  full copy deck, all four used all of it. Deciding what to leave out was not
  something the competition produced; that needed the narrower brief that came
  afterwards.
