# Kora landing page - shared design brief

You are one of several independent designers. Everyone gets this identical brief.
Your concept will be judged against the others on **design quality alone**, so the
facts and copy below are fixed: do not invent new claims to make your concept look
better. Differentiate through layout, typography, motion, visual metaphor, and
information hierarchy.

---

## 1. What Kora is (ground truth - read this carefully)

Kora is an **open-source memory engine for AI agents**. Self-hosted,
graph-first, Kubernetes-native. Apache 2.0.

The one-sentence pitch: *AI agents forget everything between sessions. Kora
gives them a persistent memory graph they can query by meaning and by
relationship, running on your own infrastructure.*

**The core technical differentiator, which the page must make land:**

Ordinary approaches store memories as flat text chunks in a vector database and
retrieve by "find similar text". Kora stores memories as a **connected graph**
and retrieves by "find what is *related*" - following typed edges - combined with
vector similarity. Concretely:

- A vector DB asked "what database does this project use?" returns every chunk
  that mentions a database, including the outdated ones, ranked by cosine
  similarity. It cannot tell you which fact is current.
- Kora returns `PostgreSQL`, plus the edge showing it **supersedes** the older
  `MySQL` fact, plus the edge showing **why** the switch happened. Contradiction
  detection marks stale facts as superseded instead of letting them pile up
  alongside the new ones.

That "supersedes" story is the single strongest idea on the page. Whatever your
concept, find a way to *show* it, not just assert it.

## 2. Audience and goal

Primary visitor: an **AI product builder evaluating memory options** - someone
building a coding assistant, support bot, or agent product who is currently
deciding between rolling their own vector store, a hosted memory SaaS, or this.

They are technical but arrive skeptical and skimming. The page must, in order:

1. Make them recognize the problem in their own product (agents are amnesiac).
2. Show why a graph beats flat vector similarity for this specific problem.
3. Prove it is real and runnable - real API, real install, real code.
4. Convert: "Read the docs" / "Get started" primary, GitHub secondary.

Install commands belong **below the fold**, not in the hero. Developers who are
already sold need them one scroll away, but the argument comes first.

## 3. Fixed copy deck

Use this copy. You may cut, re-order, and tighten it, and you may write section
headings and micro-copy of your own. Do not add new factual claims.

**Hero**
- Eyebrow: `Open source · Apache 2.0 · Self-hosted`
- Headline options (pick one or write a sharper one from the same idea):
  - `Your agent forgets everything. Give it a memory that connects.`
  - `Memory for AI agents that remembers how things relate.`
- Sub: `Kora is a graph-first memory engine. Store conversations, auto-extract
  facts, query by meaning and relationship - in your own cluster, on your own data.`
- Primary CTA: `Get started` · Secondary CTA: `View on GitHub`

**The problem** (three beats)
- `You explain your architecture to your coding assistant on Monday. On Tuesday it
  has no idea what you said.`
- `A support bot asks the same onboarding questions every time a returning
  customer reaches out.`
- `Your team runs five AI tools on one project. None of them share what they learned.`

**Why existing solutions fall short**
- Platform memory (ChatGPT, Claude): locked to one vendor, cannot be shared across tools.
- Vector databases: find similar text, but do not understand relationships between memories.
- Custom solutions: every team rebuilds the same thing, poorly.

**What it does** (three capabilities)
1. `Remember everything` - Feed it any conversation. It extracts the facts,
   preferences, and events automatically and stores them as typed nodes.
2. `Recall what matters` - Not 50 vaguely related chunks. The 3-5 memories that
   actually answer the question, with the relationships that explain them.
3. `Build user profiles` - A static profile (role, expertise, preferences) plus a
   dynamic one (last 7 days of events), maintained automatically per project.

**Comparison table** (traditional vs Kora)
| | Traditional | Kora |
|---|---|---|
| Memory structure | Flat text chunks | Connected knowledge graph |
| Retrieval | Find similar text | Follow relationships, not just similarity |
| Updates | Old info mixed with new | Contradictions detected, stale facts superseded |
| Learning | Starts fresh each session | Consolidates over time, forgets noise |
| Sharing | Locked to one tool | Any agent can plug in, framework-agnostic |
| Privacy | Your data on their servers | Self-hosted, data never leaves your infra |

**Code / API** - these are the real endpoints, use them verbatim:

```bash
curl -X POST http://localhost:8080/v1/memories/extract \
  -H "X-API-Key: $API_KEY" \
  -d '{"conversation": "user: We switched from MySQL to PostgreSQL last week because we needed graph support", "project_id": "my-project"}'
```

```python
from kora import KoraClient

client = KoraClient(endpoint="localhost:50051", api_key=..., project="my-project")

client.extract("user: We switched to PostgreSQL\nuser: I prefer vim")
results = client.query("what database does this project use?", top_k=3)
```

Real REST surface (pick a subset if you show an API section):
`POST /v1/memories`, `POST /v1/memories/extract`, `GET /v1/memories/query`,
`POST /v1/memories/connect`, `DELETE /v1/memories/{id}`,
`GET /v1/memories/{id}/graph`, `GET /v1/profiles/{project_id}`,
`POST /v1/sessions`, `GET /v1/health`, `GET /metrics`

**Install** (below the fold, real and copyable)

```bash
# Docker Compose
cat > .env <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 16)
KORA_API_KEYS=$(go run ./cmd/cli keys generate)
EOF
docker compose up
```

```bash
# Helm
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.existingSecret=my-postgres-secret \
  --set auth.existingSecret=my-api-keys
```

Also available: Python SDK (`pip install kora`) and a Go CLI
(`kora store`, `kora query`, `kora graph <id> --depth 2`).

**Stack** (every dependency OSI-approved, no SSPL/BSL/proprietary)
Go · PostgreSQL 18 · Apache AGE (graph) · pgvector (vectors) · gRPC + REST ·
React web UI with live graph visualization · Prometheus metrics · Helm chart

Note the architectural point worth making visually: **AGE and pgvector live in the
same PostgreSQL instance**, so graph traversal and vector search happen in one
database, one query path, no sync between two stores.

**Engineering rigor** (optional section - strong material, use if it fits)
The repo verifies against a live cluster, not mocks: 80 checks in
`verify_k8s.sh` covering probes, security contexts, NetworkPolicy, PDB evictions
and recoverability; a from-scratch install test; a backup/restore/verify path;
long-running soak tests; and mutation testing that checks the tests actually fail
when the code is wrong.

**Roadmap / coming soon** (these are NOT built yet - if you show them, label them clearly)
Content ingestion (PDF, URLs, code) · Framework SDKs (LangChain, CrewAI, MCP) ·
Connectors (GitHub, Drive, Notion) · Kubernetes Operator with CRDs · MemoryBench results

**Footer**
GitHub `github.com/kora/Kora` · Apache 2.0 · Docs · Contributing · Security

### Absolutely do not claim
No user counts, no "trusted by" logos, no funding, no benchmark numbers, no
latency or throughput figures, no testimonials, no "10x faster". None of these
exist. A fabricated stat kills the credibility of the whole page. If your layout
wants a stats band, use real structural facts instead (Apache 2.0, 10 REST
endpoints, one database, 80 live-cluster checks, zero proprietary dependencies).

## 4. Brand and visual constraints

Existing product UI, which the landing page should feel continuous with:

- Background: `#0a0a0f` (near-black, very slightly blue)
- Body text: `#e1e2e8`
- Brand purple: `#863bff`, deep variant `#7e14ff`, pale `#ede6ff`
- Accent cyan: `#47bfff`
- Font: Inter (system fallbacks) - the product UI uses it. You may introduce a
  display face for headings if it strengthens the concept, loaded from Google
  Fonts or a system stack.
- The logo mark is a stylized lightning bolt / zigzag arrow in purple with a soft
  glow. It is at `web/public/favicon.svg` in the repo - read it and reuse it.
- Dark theme is the default and the expectation. A light mode is not required.

Design in the register of **modern developer-infrastructure companies** - think
the visual seriousness of Linear, Neon, Vercel, Resend, Clerk, Supabase. Precise
typography, restrained color, generous whitespace, real product truth over stock
illustration. Avoid: stock photos of people, generic AI brain imagery, cartoon
robots, purple-gradient-on-everything, glassmorphism for its own sake.

## 5. Technical requirements for your deliverable

Write **one self-contained HTML file**, no build step, opens correctly with
`file://`:

- Tailwind via `<script src="https://cdn.tailwindcss.com"></script>` is fine, as
  is hand-written CSS in a `<style>` block. Either is acceptable.
- All animation and interaction in inline `<script>`. No local file imports.
- Any icons: inline SVG. No icon-font CDNs.
- It must be a **complete page**: nav, hero, every major section, footer. Not a
  fragment, not a mood board.
- **Responsive.** It will be screenshotted at 1440px and at 390px. Both must look
  deliberate. No horizontal overflow at any width - check nested flex/grid
  children get `min-width: 0`.
- Respect `prefers-reduced-motion` for anything that animates.
- Semantic HTML and real focus states. Contrast must pass WCAG AA on body text.
- Target roughly 900-1600 lines. Rich, but every line earning its place.

## 6. How you will be judged

1. **Does the graph-vs-vector idea land in the first two screens?** Weighted heaviest.
2. Visual craft: typography scale, spacing rhythm, color discipline, alignment.
3. Originality of the central visual metaphor. Four designers are working on this
   in parallel - a generic dark SaaS page loses to a concept with a real idea.
4. Restraint. Motion that serves comprehension beats motion that decorates.
5. Mobile quality, treated as a real design, not a stacked afterthought.

## 7. Output

Write your file to the exact path given in your individual assignment, then reply
with:
- Your concept name and its central idea in two sentences.
- The one design decision you would defend hardest.
- Anything you would change with more time.

Do not write to any other path. Do not modify the repository. Do not run git.
