# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Context0, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email: **security@context0.dev** (or open a private security advisory on GitHub)

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

## Security Practices

Context0 follows these security practices:

- **No hardcoded secrets**: all credentials via environment variables or K8s Secrets
- **API key authentication**: with token bucket rate limiting
- **Input validation**: on all API endpoints
- **SQL injection prevention**: parameterized queries (pgx) + Cypher escaping
- **Dependency scanning**: automated via CI (trivy, gosec)
- **Container security**: distroless base images, non-root user
- **Network isolation**: K8s NetworkPolicies in production mode
