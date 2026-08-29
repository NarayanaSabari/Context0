# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Kora, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email: **security@kora.dev** (or open a private security advisory on GitHub)

### What to include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response timeline

- **Acknowledgment**: within 48 hours
- **Assessment**: within 1 week
- **Fix**: within 30 days for critical issues

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.x.x (latest) | Yes |
| Older versions | No |

## What an API key grants

**One Kora deployment is one trust domain.** An API key is not scoped to a
project: any valid key can read, write and delete every memory the deployment
holds, in every project.

This is deliberate rather than an oversight, and it is recorded in
[ADR 0002](docs/adr/0002-one-deployment-is-one-trust-domain.md). Kora's value
comes from agents sharing what they learn, and the self-hosted deployment model
already puts the boundary at the cluster: whoever installs the chart decides
who gets a key. Per-project keys would add an authorisation model to every call
path in exchange for a boundary the deployment topology already provides.

Two consequences worth stating plainly:

- **Projects organise retrieval; they do not isolate it.** `project_id` decides
  which memories a query searches, not which memories a caller is allowed to
  see. A caller who omits it queries across every project in the deployment,
  which is the documented behaviour of `GET /v1/memories/query`.
- **Separate trust domains need separate deployments.** That is the supported
  answer and it is cheap here: one chart install and one database per domain.
  Do not hand a key for a shared deployment to a party that should not read
  everything in it.

If you need multi-tenant isolation within one deployment, it is a real feature
with its own design -- key-to-project binding, a project-scoped query path, and
audit -- rather than a configuration flag. Say so in an issue rather than
assuming a per-project key exists.

## Security Practices

Kora follows these security practices:

- **No hardcoded secrets**: all credentials via environment variables or K8s Secrets
- **API key authentication**: with token bucket rate limiting, deployment-wide
  in scope (see [What an API key grants](#what-an-api-key-grants))
- **Input validation**: on all API endpoints
- **SQL injection prevention**: parameterized queries (pgx) + Cypher escaping
- **Dependency scanning**: automated via CI (trivy, gosec)
- **Container security**: distroless base images, non-root user
- **Network isolation**: K8s NetworkPolicies in production mode
