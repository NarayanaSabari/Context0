#!/usr/bin/env python3
"""Build the offline evaluation fixtures under eval/fixtures/locomo.

Run once. Everything `make eval` consumes is written here so that the eval
itself never touches a network or a model server.

Inputs (all local):
  - the LoCoMo dataset, data/benchmarks/locomo/locomo10.json in a memorybench
    checkout (--locomo)
  - the pinned 200-question id list from the memorybench fork (--pinned)
  - the Postgres container holding the corpus behind the published 0.690 run
    (--abl-container), whose GLM-extracted memories, entities and nomic
    embeddings are dumped verbatim

Outputs:
  eval/data/corpus-extracted.json              the 0.690 run's memories. Gitignored:
                                               LoCoMo is CC BY-NC 4.0 and this is
                                               adapted from its text
  eval/fixtures/locomo/labels-extracted.json   memory id -> source turn alignment (committed)
  eval/fixtures/locomo/embeddings.bin          float32 vectors keyed by sha256(text) (committed)

This is a one-time snapshot: the database it reads was produced by GLM
extraction over the OpenRouter API and cannot be rebuilt offline. The turns
corpus and the questions are embedded separately by `go run ./cmd/eval
fixtures`, which uses the engine's own Ollama client. Verified that the client
reproduces the stored vectors: cosine 1.000000, max abs diff 1.2e-7, and
repeated calls are bit-identical.
"""
import argparse
import hashlib
import json
import os
import re
import struct
import subprocess
import sys
from collections import defaultdict

CATEGORY = {1: "single-hop", 2: "multi-hop", 3: "temporal", 4: "world-knowledge", 5: "adversarial"}
MONTHS = ["january", "february", "march", "april", "may", "june", "july",
          "august", "september", "october", "november", "december"]

# The ten conversation projects of run kora-abl-on, hashed the way the
# memorybench adapter hashes `<conversation>-<runId>` (projectIdFor).
ABL_PROJECTS = {
    "mb1d3m5ms1o75zoq": "conv-26", "mbybgpkvno9m6p": "conv-30",
    "mbis1pjrton4dx": "conv-41", "mbmctezu1u7auc0": "conv-42",
    "mbga4l8xvouiaz": "conv-43", "mb25kaoc1w7i892": "conv-44",
    "mb15w2iq5zp9a53": "conv-47", "mb1f3rjdk16sy42": "conv-48",
    "mbt5tm7z11pgo25": "conv-49", "mb1slj27pxot8o3": "conv-50",
}


def parse_locomo_date(s):
    """Port of memorybench's parseLocomoDate: returns (iso, formatted)."""
    m = re.match(r"(\d+):(\d+)\s*(am|pm)\s*on\s*(\d+)\s*(\w+),?\s*(\d+)", s, re.I)
    if not m:
        return None, None
    hour, minute, ampm, day, month_name, year = m.groups()
    hour = int(hour)
    if ampm.lower() == "pm" and hour != 12:
        hour += 12
    if ampm.lower() == "am" and hour == 12:
        hour = 0
    month = next(i for i, n in enumerate(MONTHS) if n.startswith(month_name.lower()))
    iso = "%04d-%02d-%02dT%02d:%02d:00.000Z" % (int(year), month + 1, int(day), hour, int(minute))
    formatted = "%d:%s %s on %s %s, %s" % (hour % 12 or 12, minute, "pm" if hour >= 12 else "am", day, month_name, year)
    return iso, formatted


def render_turn(formatted_date, speaker, text):
    """Port of the adapter's renderTranscript for one utterance."""
    content = re.sub(r"\s*\n\s*", " ", text).strip()
    prefix = f"On {formatted_date}, {speaker} said that" if formatted_date else f"{speaker} said that"
    return f"{prefix} {content}"


def parse_evidence(raw):
    """LoCoMo evidence is a list of 'D<session>:<turn>'. Three of the pinned
    questions deviate: two carry several ids in one string, one is
    zero-padded. Both are normalised rather than dropped."""
    out = []
    for item in raw or []:
        for tok in item.split():
            m = re.fullmatch(r"D(\d+):(\d+)", tok)
            if m:
                out.append("D%d:%d" % (int(m.group(1)), int(m.group(2))))
    return out


def load_dataset(path, pinned_path):
    data = json.load(open(path))
    pinned = json.load(open(pinned_path))
    pinned_set = set(pinned)
    questions, turns = [], []
    for conv in data:
        cid = conv["sample_id"]
        c = conv["conversation"]
        for i in range(1, 101):
            key = f"session_{i}"
            if key not in c:
                break
            iso, formatted = parse_locomo_date(c.get(f"{key}_date_time", "")) if c.get(f"{key}_date_time") else (None, None)
            for msg in c[key]:
                turns.append({
                    "dia_id": msg["dia_id"], "conversation": cid, "session": i,
                    "date": iso, "speaker": msg["speaker"], "text": msg["text"],
                    "content": render_turn(formatted, msg["speaker"], msg["text"]),
                })
        for i, qa in enumerate(conv["qa"]):
            qid = f"{cid}-q{i}"
            if qid not in pinned_set:
                continue
            questions.append({
                "id": qid, "conversation": cid, "category": CATEGORY[qa["category"]],
                "question": qa["question"], "answer": str(qa.get("answer", "")),
                "adversarial_answer": qa.get("adversarial_answer", ""),
                "evidence": parse_evidence(qa.get("evidence")),
            })
    order = {q: i for i, q in enumerate(pinned)}
    questions.sort(key=lambda q: order[q["id"]])
    return questions, turns


def psql(container, sql):
    # graphid has no equality operator outside ag_catalog, so the search path
    # has to name it for the edge joins below.
    sql = 'SET search_path = ag_catalog, "$user", public; ' + sql
    out = subprocess.run(["docker", "exec", container, "psql", "-q", "-U", "kora", "-d", "kora", "-At", "-F", "\t", "-c", sql],
                         capture_output=True, text=True, check=True)
    return [line.split("\t") for line in out.stdout.splitlines() if line]


PROP = "(ag_catalog.agtype_access_operator(VARIADIC ARRAY[{alias}.properties, '\"{name}\"'::ag_catalog.agtype]))::text"


def dump_extracted(container):
    projects = "','".join(ABL_PROJECTS)
    memories = {}
    rows = psql(container, f"""
        SELECT m.properties::text FROM context0."Memory" m
        WHERE {PROP.format(alias='m', name='project_id')} IN ('{projects}')""")
    for (props,) in rows:
        p = json.loads(props)
        memories[p["id"]] = {
            "id": p["id"], "conversation": ABL_PROJECTS[p["project_id"]],
            "content": p["content"], "type": p["type"],
            "tags": json.loads(p["tags"]) if p.get("tags") else [],
            "created_at": p["created_at"], "entities": [],
        }
    rows = psql(container, f"""
        SELECT {PROP.format(alias='m', name='id')}, {PROP.format(alias='e', name='name')}
        FROM context0.mentions r
        JOIN context0."Memory" m ON m.id = r.start_id
        JOIN context0."Entity" e ON e.id = r.end_id
        WHERE {PROP.format(alias='m', name='project_id')} IN ('{projects}')
        ORDER BY r.id""")
    for mid, name in rows:
        if mid in memories:
            memories[mid]["entities"].append(name)
    vectors = {}
    rows = psql(container, f"""
        SELECT memory_id, embedding::text FROM public.memory_embeddings
        WHERE project_id IN ('{projects}')""")
    for mid, vec in rows:
        if mid in memories:
            vectors[memories[mid]["content"]] = json.loads(vec)
    missing = [m for m in memories.values() if m["content"] not in vectors]
    corpus = sorted(memories.values(), key=lambda m: m["id"])
    return corpus, vectors, missing


TOKEN = re.compile(r"[a-z0-9]+")
STOP = set("""a an the and or but of to in on at for with from by as is are was were be been being
have has had do does did i you he she it we they me him her us them my your his its our their
this that these those there here what which who whom when where why how not no yes so if then
than too very can could would should will just also said says say about into over after before
up down out off again once more most some such only own same other each few both all any
""".split())


def tokens(text):
    return {t for t in TOKEN.findall(text.lower()) if t not in STOP and len(t) > 1}


DATE_IN_TEXT = re.compile(r"\b(\d{1,2}) (January|February|March|April|May|June|July|August|September|October|November|December),? (\d{4})\b")


def align(corpus, turns):
    """Map each extracted memory to the turn(s) it most plausibly came from.

    Deterministic and heuristic. Score is token containment: the share of a
    memory's content words found in a turn. A date in the memory that matches
    a session date favours that session, because the adapter inlined the
    session date into every utterance and the extractor kept it. A memory
    keeps every turn within 0.1 of its best score and above 0.35, so a memory
    merged from two turns can align to both.

    Calibrated against the LLM judge's hit@10 labels on the stored top-10s of
    run kora-abl-on: this setting agrees with the judge on 66% of questions
    (13 questions the labels call a hit the judge did not, 53 the reverse).
    Folding each turn's predecessor into its token set, an earlier variant,
    dropped agreement to 53%. The residual disagreement is structural: 39 of
    the 200 questions have no evidence turn represented in the extracted
    corpus at all, and the judge credits paraphrases these labels cannot see.
    The verbatim-turns corpus carries exact labels and is the primary
    metric; these labels are the secondary one."""
    by_conv = defaultdict(list)
    for t in turns:
        by_conv[t["conversation"]].append(t)
    session_dates = {}
    for t in turns:
        if t["date"]:
            y, mo, d = t["date"][:10].split("-")
            session_dates[(t["conversation"], t["session"])] = (int(d), MONTHS[int(mo) - 1], int(y))
    labels, unaligned = {}, 0
    for mem in corpus:
        mtoks = tokens(mem["content"])
        if not mtoks:
            unaligned += 1
            continue
        conv_turns = by_conv[mem["conversation"]]
        dated_sessions = set()
        for d, month, y in DATE_IN_TEXT.findall(mem["content"]):
            for (c, s), (sd, sm, sy) in session_dates.items():
                if c == mem["conversation"] and (int(d), month.lower(), int(y)) == (sd, sm, sy):
                    dated_sessions.add(s)
        scored = []
        for t in conv_turns:
            ttoks = tokens(t["text"])
            score = len(mtoks & ttoks) / len(mtoks)
            if dated_sessions and t["session"] in dated_sessions:
                score += 0.15
            scored.append((score, t["dia_id"]))
        scored.sort(reverse=True)
        best = scored[0][0]
        keep = [d for s, d in scored if s >= max(0.35, best - 0.1)] if best >= 0.35 else []
        if keep:
            labels[mem["id"]] = keep[:3]
        else:
            unaligned += 1
    return labels, unaligned


def write_embeddings(path, vectors, dim):
    keys = sorted(vectors)
    with open(path, "wb") as f:
        f.write(b"KEMB")
        f.write(struct.pack("<II", dim, len(keys)))
        for text in keys:
            vec = vectors[text]
            assert len(vec) == dim, (len(vec), text[:40])
            f.write(hashlib.sha256(text.encode()).digest())
            f.write(struct.pack("<%df" % dim, *vec))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--locomo", default=os.path.expanduser("~/Developer/narayana/memorybench/data/benchmarks/locomo/locomo10.json"))
    ap.add_argument("--pinned", default=os.path.expanduser("~/Developer/narayana/memorybench/data/pinned/locomo-200.json"))
    ap.add_argument("--abl-container", default="kora-abl-pg")
    ap.add_argument("--fixtures", default="eval/fixtures/locomo",
                    help="committed outputs: labels and embeddings (no dataset text)")
    ap.add_argument("--data", default="eval/data",
                    help="gitignored outputs derived from the CC BY-NC dataset text")
    args = ap.parse_args()
    os.makedirs(args.fixtures, exist_ok=True)
    os.makedirs(args.data, exist_ok=True)

    questions, turns = load_dataset(args.locomo, args.pinned)
    print(f"questions={len(questions)} turns={len(turns)}", file=sys.stderr)

    corpus, vectors, missing = dump_extracted(args.abl_container)
    json.dump(corpus, open(f"{args.data}/corpus-extracted.json", "w"), indent=1)
    labels, unaligned = align(corpus, turns)
    json.dump(labels, open(f"{args.fixtures}/labels-extracted.json", "w"), indent=1, sort_keys=True)
    print(f"extracted memories={len(corpus)} without embedding={len(missing)} aligned={len(labels)} unaligned={unaligned}", file=sys.stderr)

    # Only the snapshot's own vectors are written here. The turns and the
    # questions are embedded by `go run ./cmd/eval fixtures`, through the
    # engine's own Ollama client, so the fixture is built by the code it
    # serves. The Go tool merges into this file rather than replacing it.
    dim = len(next(iter(vectors.values())))
    write_embeddings(f"{args.fixtures}/embeddings.bin", vectors, dim)
    print(f"snapshot embeddings={len(vectors)} dim={dim}", file=sys.stderr)


if __name__ == "__main__":
    main()
