# Apache AGE is a load-bearing choice, not a pluggable one

The original MVP plan justified Apache AGE partly on being swappable: a graph repository interface would keep another graph database one implementation away.
That is no longer true, and pretending otherwise misleads anyone reading the code.
AGE-shaped constraints have spread into how the engine works: parameters cannot always be bound, so identifiers and content hashes are validated and inlined into Cypher literals (`isPlainUUID`, `IsContentHash`); AGE offers no index that can serve a substring predicate, which is why keyword search moved to PostgreSQL full-text search alongside the graph rather than inside it; and hybrid retrieval depends on pgvector living in the same PostgreSQL instance as the graph.
Swapping the graph store now means rewriting retrieval, not reimplementing an interface.

So: AGE on PostgreSQL is a fixed architectural commitment.
Interfaces at the service boundary exist to give tests a seam, and are not a portability claim.

## Consequences

- The graph repository's tests must run against a real AGE instance, since a query can be valid Go and malformed openCypher.
- A proposal to change graph stores is a rewrite proposal and should be sized as one.
