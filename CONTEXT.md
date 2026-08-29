# Kora

Kora is a memory engine for AI agents: it stores what an agent learns, keeps the store from accumulating restatements of the same thing, and returns the few memories that answer a question.

## Language

### Storage

**Memory**:
One stored statement an agent may later need to recall, with a type, an owning project, and a time.
_Avoid_: record, item, chunk, document

**Episodic**:
A memory of something that happened at a time, such as a decision taken or a conversation held.
_Avoid_: event log, history entry

**Semantic**:
A memory of something believed to be true independent of when it was learned.
_Avoid_: fact (too generic to disambiguate from the other two types in prose)

**Procedural**:
A memory of how something is habitually done.
_Avoid_: rule, pattern, skill

**Entity**:
A named thing a memory refers to, held as its own node so unrelated memories about the same thing can be reached from one another.
_Avoid_: keyword, tag, noun

### Scope

**Deployment**:
One running Kora installation, and one trust domain: every API key in it can read every project it holds.
_Avoid_: instance, cluster, tenant

**Project**:
The scope a memory belongs to and a query is normally answered within. It organises retrieval; it is not a security boundary.
_Avoid_: tenant, workspace, namespace, org

**Agent**:
The client that stores and queries memories. Agents share a project's memories rather than owning private ones.
_Avoid_: client, consumer, user

**Session**:
One agent's continuous run within a project, used to group the memories it produced.
_Avoid_: conversation, thread, run

### Change over time

**Fold**:
Merging a newly written memory into an existing one that already says the same thing, at write time, so the restatement is never stored separately.
_Avoid_: dedup, write-time consolidation, upsert

**Consolidation**:
The background maintenance cycle over memories already stored: merging duplicates, decaying scores, pruning what has gone stale. Never used for the write-time behaviour, which is Fold.
_Avoid_: compaction, garbage collection, cleanup

**Supersede**:
The relationship a newer memory has to an older one it contradicts. The older memory is kept and marked, not deleted.
_Avoid_: overwrite, invalidate, replace

**Profile**:
The view of a project's memories split into stable knowledge and recent activity, offered so an agent can open a conversation already informed.
_Avoid_: summary, persona, user model
