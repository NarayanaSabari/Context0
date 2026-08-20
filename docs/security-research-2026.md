# Enterprise-Grade Security Research: Kora (Go + PostgreSQL on Kubernetes)

Research-only report. No code was modified.
Date: 2026-08-18. Every substantive claim is cited. "Inference" marks my judgment, not a sourced fact.

---

## 0. Findings from the current tree (verified by reading the files)

| Observation | Location |
|---|---|
| API keys held as `map[string]bool`, validated by `a.validKeys[key]` | `internal/auth/apikey.go:38,104,143` |
| Empty key set silently disables auth entirely | `internal/auth/apikey.go:83,131` |
| Any path not under `/v1/` bypasses auth | `internal/auth/apikey.go:125` |
| Token buckets keyed by raw API key, unbounded map, never evicted | `internal/auth/apikey.go:48,160-171` |
| Hardcoded default keys `ctx0_dev_key_1,ctx0_dev_key_2` | `charts/kora/values.yaml:105` |
| Hardcoded Postgres password `kora-dev-password` | `charts/kora/values.yaml:54` |
| **Postgres password is inlined into a plain env var in the Deployment, not read from the Secret** | `charts/kora/templates/api.yaml:68` |
| `sslmode=disable` to Postgres | `charts/kora/templates/api.yaml:68` |
| Pod already: `runAsNonRoot`, `runAsUser: 1000`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `drop: ["ALL"]` | `charts/kora/templates/api.yaml:46-60` |
| CI already runs gosec + Trivy image scan + dependency-review; **no SBOM, no signing, no provenance** | `.github/workflows/security.yaml` |

Two things worth flagging that were not in the brief:

1. **The Secret `postgres-age-secret` is created but the API never uses it.** The chart templates the password directly into `KORA_DATABASE_URL` as a literal env value. So the credential is visible in `kubectl get deploy -o yaml`, in `helm get manifest`, in any GitOps repo, and to anyone with `get pods` in the namespace. This is strictly worse than the Secret path and is a one-line-class bug, not a design tradeoff.
2. **Unbounded rate-limiter map keyed by attacker-controlled input.** `allowRequest` is only reached after key validation, so it is not directly a memory-exhaustion vector today - but if key validation is ever moved or the "no keys configured" branch is hit, every distinct key string allocates a bucket forever. Inference: worth a bounded LRU regardless.

---

## 1. API key storage and verification

### 1.1 Why the current comparison is wrong - two separate problems

**Problem A: timing.** Go's `map[string]bool` lookup hashes the key and then compares candidate strings with the runtime's `memequal`, which short-circuits on the first differing byte. Go's own standard library documents that the fix for secret comparison is `crypto/subtle`: `ConstantTimeCompare` "returns 1 if the two slices, x and y, have equal contents and 0 otherwise. The time taken is a function of the length of the slices and is independent of the contents." (<https://pkg.go.dev/crypto/subtle>)

Honest calibration: remotely exploiting a byte-at-a-time timing oracle across a network, through gRPC-gateway, against a Go map with a randomized hash seed, is *hard*. Nanosecond differences are buried under network jitter. So this is not the most urgent finding. But it is free to fix, and OWASP treats non-constant-time comparison of secrets as a defect regardless of measured exploitability. The real reason to fix it is that the correct fix (1.2) also solves Problem B.

**Problem B: at-rest exposure - this is the serious one.** The keys exist in plaintext in:

- `values.yaml` in the public git repo (default keys)
- the Helm release Secret and rendered manifests
- etcd, unencrypted by default: "Kubernetes Secrets are, by default, stored unencrypted in the API server's underlying data store (etcd). Anyone with API access can retrieve or modify a Secret, and so can anyone with access to etcd." (<https://kubernetes.io/docs/concepts/configuration/secret/>)
- the process environment of every API pod, readable from `/proc/self/environ`, and typically dumped by crash handlers and observability agents

Consequence: a single read of the Secret, an etcd backup, a heap dump, or a leaked `helm get values` yields *usable credentials*. There is no revocation story and no way to tell which key was used.

### 1.2 The correct scheme for API keys (not passwords)

API keys differ from user passwords in one decisive way: **the server generates them with full entropy.** A password is chosen by a human and may have 20-30 bits of entropy, which is why it must be fed through a deliberately slow KDF - the KDF is buying back entropy the user failed to provide. NIST SP 800-63B requires memorized secrets be stored with "a suitable one-way key derivation function... The chosen output length of the key derivation function SHOULD be the same as the length of the underlying one-way function output" and explicitly frames the cost factor as being about "the difficulty of a brute force attack" on low-entropy human secrets (<https://pages.nist.gov/800-63-3/sp800-63b.html>, §5.1.1.2). None of that reasoning applies to a 256-bit random token: there is no brute-force search space to slow down.

**Recommended scheme (industry-standard, Stripe/GitHub shape):**

```
ctx0_live_<22-char base62 id>_<43-char base64url secret>
└──┬─┘ └─┬─┘ └────────┬─────┘ └───────────┬───────────┘
  prefix env      lookup id            secret (256 bits)
```

Store: `id` (indexed, plaintext) plus `SHA-256(secret)` or `HMAC-SHA256(pepper, secret)`. Verify:

1. Parse the prefix; reject anything not matching the expected shape before touching the database.
2. Look up the row by `id` - one indexed point query, no scan over all keys.
3. `subtle.ConstantTimeCompare(storedHash, sha256(presentedSecret)) == 1`.

**Why the searchable prefix.** GitHub's engineering post on their token formats is the primary source: prefixes make tokens "identifiable" for secret scanning, they chose `_` as separator because it "is not a Base64 character which helps ensure that our tokens cannot be accidentally duplicated by randomly generated strings like SHAs," and they added a CRC32 checksum in the last 6 characters because "a checksum virtually eliminates false positives for secret scanning offline. We can check the token input matches the checksum and eliminate fake tokens without having to hit our database." With the prefix alone they expected the secret-scanning false-positive rate to drop to 0.5%. (<https://github.blog/engineering/platform-security/behind-githubs-new-authentication-token-formats/>)

That post also explicitly credits Stripe and Slack as prior art for the pattern. The practical payoffs for Kora: GitHub secret scanning can be taught the `ctx0_` pattern; a leaked key in a public repo becomes detectable; and the id/secret split means the server can look up one row instead of comparing against every key.

### 1.3 bcrypt/argon2 vs HMAC-SHA256 vs plain SHA-256

| Scheme | Verify cost | Right for API keys? |
|---|---|---|
| bcrypt / scrypt / Argon2id | Deliberately 50-250ms and/or 64MB+ RAM | **No.** Correct for passwords, a self-inflicted DoS here. |
| HMAC-SHA256 with a server-side pepper | ~1µs | **Yes.** Best option if you can hold a pepper outside the DB. |
| Plain SHA-256 | ~0.5µs | **Yes, acceptable** for 256-bit random secrets. |

**The DoS argument, concretely.** Argon2's own reference guidance (RFC 9106, §4) recommends parameters on the order of 1 GiB of memory with 1 iteration, or 64 MiB with 3 iterations, for the recommended configurations (<https://www.rfc-editor.org/rfc/rfc9106.html#section-4>). Kora's API pod is capped at 512Mi memory and 500m CPU (`values.yaml:28-34`). Running a memory-hard KDF on *every request* would either not fit in the memory limit at all or would reduce throughput to single-digit requests per second per replica. An unauthenticated attacker sending garbage keys would then trivially saturate the CPU - the auth check runs *before* any rate limiting can help, because rate limiting is keyed on a validated key. **A slow KDF in the request path converts a cheap authentication check into an amplification vector.** This is the entire reason the industry standard for API keys is a single fast hash.

**Why a fast hash is safe here, and only here.** The security of plain SHA-256 over the token depends entirely on the token having full entropy. With 256 bits from `crypto/rand`, an offline attacker holding the hash database has nothing to brute-force - there is no dictionary, no rainbow table, no human-chosen pattern. This is the same argument that justifies storing session tokens as bare hashes. It collapses immediately if anyone ever generates keys from a low-entropy source, which is why key generation must be `crypto/rand` and must be enforced in code rather than left to the operator.

**HMAC vs plain SHA-256.** HMAC with a pepper stored outside the database (env var, KMS) means a database-only compromise - SQL injection, a stolen backup, a leaked read replica - does not yield offline-verifiable material. OWASP's Password Storage guidance describes peppering as an additional defense layer applied on top of hashing, using an HMAC with a secret held separately from the hash store (<https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>). For Kora the pepper is one more secret to manage, and the pepper cannot be rotated without re-hashing every key. Inference: **start with SHA-256, design the schema with an `algo` column so HMAC can be introduced later without a migration.**

### 1.4 Minimal correct Go shape

```go
// Generation - crypto/rand is non-negotiable.
secret := make([]byte, 32)
if _, err := rand.Read(secret); err != nil { return err }
display := "ctx0_live_" + id + "_" + base64.RawURLEncoding.EncodeToString(secret)
// Store only: id, sha256(secret), created_at, last_used_at, revoked_at, project_id.
// Return `display` exactly once. It is never recoverable afterwards.

// Verification
sum := sha256.Sum256(presentedSecret)
if subtle.ConstantTimeCompare(rec.Hash, sum[:]) != 1 {
    return errUnauthenticated // identical error and identical latency as "id not found"
}
```

Three details that are easy to get wrong:

- Return the **same** error and ideally the same latency for "unknown id" and "wrong secret". Otherwise the id lookup itself becomes an enumeration oracle.
- `subtle.ConstantTimeCompare` "returns 0 immediately" on a length mismatch (<https://pkg.go.dev/crypto/subtle>). Fine for fixed-32-byte hashes; would leak length if applied to variable-length input, which is another reason to compare hashes rather than raw keys.
- Go 1.24+ adds `subtle.WithDataIndependentTiming`, which enables `PSTATE.DIT` on Arm64 processors with FEAT_DIT so that "the timing of specific instructions is independent of their inputs" (<https://pkg.go.dev/crypto/subtle>). Inference: nice-to-have, not required, since `ConstantTimeCompare` is already branch-free.

### 1.5 The `!strings.HasPrefix(r.URL.Path, "/v1/")` bypass

`internal/auth/apikey.go:125` allows *any* request whose path is not under `/v1/` through with no authentication. This is a deny-by-exception design, and it fails open by construction: every future endpoint added outside `/v1/` (a `/v2/`, an admin route, a debug handler, a pprof mount) is unauthenticated by default and nobody will notice. OWASP's Authorization guidance is explicit that access control should "deny by default", with exceptions enumerated rather than the reverse (<https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html>). Inference: this is a higher-severity finding than the timing issue and should be inverted to an explicit allowlist of exempt paths (`/v1/health`, `/livez`, `/readyz`, `/startupz`, `/metrics`).

Related: `/metrics` is unauthenticated (`apikey.go:125`). Prometheus metrics from this service will include per-project cardinality. Inference: verify no label carries user content or project identifiers that constitute customer data.

---

## 2. Kubernetes secret management in 2026

### 2.1 Why plain Secrets are not enough

Kubernetes documents the limitation itself, quoted in §1.1: unencrypted in etcd by default, readable by anyone with API access or the ability to create a Pod in the namespace (<https://kubernetes.io/docs/concepts/configuration/secret/>). The Good Practices page adds: "Base64 encoding is not an encryption method, it provides no additional confidentiality over plain text," and recommends configuring encryption at rest, least-privilege RBAC, restricting Secret access to specific containers, and considering external secret store providers (<https://kubernetes.io/docs/concepts/security/secrets-good-practices/>).

Note that all four of those recommendations are **cluster-operator** responsibilities. Kora ships a chart, not a cluster. That bounds what this project can actually fix - see 2.3.

### 2.2 The four options

**External Secrets Operator (ESO).** CRD-driven: a `SecretStore` says *how* to reach an external API (AWS Secrets Manager, Vault, GCP, Azure Key Vault); an `ExternalSecret` says *what* to fetch; the controller creates and continuously reconciles a native `Secret`. "If the secret from the external API changes, the controller will reconcile the state in the cluster and update the secrets accordingly." (<https://external-secrets.io/latest/introduction/overview/>) ESO's own docs warn that it "runs as a deployment in your cluster with elevated privileges. It will create/read/update secrets in all namespaces" - so ESO itself becomes a high-value target. Best fit when the operator already runs a cloud secret manager.

**Sealed Secrets (bitnami-labs).** "Encrypt your Secret into a SealedSecret, which is safe to store - even inside a public repository. The SealedSecret can be decrypted only by the controller running in the target cluster and nobody else (not even the original author)." (<https://github.com/bitnami-labs/sealed-secrets>) Asymmetric: `kubeseal` encrypts with the controller's public cert, only the in-cluster controller holds the private key. Best fit for pure-GitOps self-hosters with no cloud secret manager. Note the project moved from `bitnami-labs` to the `bitnami` org.

**SOPS (Mozilla/getsops).** Encrypts YAML/JSON values in place - keys stay readable, values are encrypted - backed by age, PGP, or a cloud KMS. Integrates natively with Flux and via a plugin with ArgoCD (<https://github.com/getsops/sops>). Lightest-weight, no in-cluster controller required, but decryption keys must reach the CD system somehow.

**Secrets Store CSI Driver.** "Allows Kubernetes to mount multiple secrets, keys, and certs stored in enterprise-grade external secrets stores into their pods as a volume." Providers: AWS, Azure, GCP, Vault, Conjur, Akeyless, OpenBao. Core mount functionality is stable; **"Auto rotation of mounted contents and synced Kubernetes secret"** is listed under *Alpha Functionality* (<https://secrets-store-csi-driver.sigs.k8s.io/>). The distinctive property is that the secret can be mounted as a tmpfs volume *without ever creating a Kubernetes Secret object*, which removes etcd from the threat model entirely. The alpha rotation status is the main caveat.

Inference on choosing: ESO if the user has a cloud secret manager; Sealed Secrets if pure GitOps and self-hosted; CSI driver if the requirement is "the secret must never be in etcd". Kora should not pick for them - it should make all four possible.

### 2.3 The minimum bar for a public chart

This is the practical crux. **The chart must not ship working default credentials, and must still be one command to install.** The reconciliation is: generate randomness at install time, or fail loudly, but never ship a constant.

Three viable patterns, ordered by how much they cost the project:

**Pattern A - fail closed with a helpful message (strongest, and my recommendation).**

```yaml
{{- if not .Values.auth.existingSecret }}
{{- if not .Values.auth.apiKeys }}
{{- fail "auth.apiKeys or auth.existingSecret is required. Generate one with:\n  kubectl create secret generic kora-api-keys --from-literal=keys=$(openssl rand -hex 32)\nthen set auth.existingSecret=kora-api-keys" }}
{{- end }}
{{- end }}
```

Helm's `fail` aborts template rendering with the message (<https://helm.sh/docs/chart_template_guide/function_list/#fail>). Cost: `helm install` is no longer zero-argument. Benefit: it is impossible to end up in production with a key that is published in the project's own git history. Inference: for a *memory engine holding user data*, the failure mode of a silent default is severe enough that the extra flag is the right trade.

**Pattern B - generate at install, persist across upgrades.** Helm's `randAlphaNum` plus `lookup` to avoid regenerating on every `helm upgrade`:

```yaml
{{- $existing := lookup "v1" "Secret" .Release.Namespace "kora-api-keys" }}
{{- $key := "" }}
{{- if $existing }}
{{-   $key = index $existing.data "keys" | b64dec }}
{{- else }}
{{-   $key = printf "ctx0_live_%s" (randAlphaNum 32) }}
{{- end }}
```

This works and keeps `helm install` frictionless. Two real caveats: `lookup` returns an empty map during `helm template` and `--dry-run`, so rendered-manifest GitOps flows (ArgoCD without server-side apply) will churn the secret on every sync; and the generated key must be surfaced in `NOTES.txt` or the user cannot use it. Helm documents the `lookup` dry-run behavior (<https://helm.sh/docs/chart_template_guide/functions_and_pipelines/#using-the-lookup-function>).

**Pattern C - `existingSecret` passthrough (necessary regardless of A or B).** Every serious chart supports pointing at a pre-existing Secret so that ESO/Sealed Secrets/SOPS/CSI users can bring their own. This is the single change that makes all four tools in 2.2 usable and costs almost nothing:

```yaml
auth:
  apiKeys: ""            # dev only; mutually exclusive with existingSecret
  existingSecret: ""     # name of a Secret with key `keys`
postgres:
  password: ""
  existingSecret: ""     # name of a Secret with key `password`
```

**And unconditionally: stop inlining the password into the env var.** `api.yaml:68` must become a `secretKeyRef`. Since `KORA_DATABASE_URL` is a composed DSN, either compose it in the application from separate `PGPASSWORD`-style parts, or template the whole DSN into the Secret and reference it as one key. Inference: the latter is less invasive to Go code; the former is cleaner long-term.

Additional hardening the chart could adopt: mount secrets as files rather than env vars. Env vars leak through `/proc/self/environ`, crash dumps, `kubectl describe`, and child processes; file mounts are readable only by the container and can be rotated in place. The Kubernetes docs recommend restricting secret access to specific containers via volume mount or env var configuration so other containers cannot see them (<https://kubernetes.io/docs/concepts/security/secrets-good-practices/>).

---

## 3. NetworkPolicy

### 3.1 Semantics that determine the design

From the Kubernetes NetworkPolicy docs (<https://kubernetes.io/docs/concepts/services-networking/network-policies/>), the four rules that matter here:

1. Policies are **additive** and never conflict: "the connections allowed in that direction from that pod is the union of what the applicable policies allow. Thus, order of evaluation does not affect the policy result."
2. **Both ends must permit**: "For a connection from a source pod to a destination pod to be allowed, both the egress policy on the source pod and the ingress policy on the destination pod need to allow the connection."
3. A pod is only isolated once *some* policy selects it in that direction. Hence the empty-`podSelector` default-deny.
4. **The DNS trap**: "A default deny-all egress policy also blocks DNS traffic. If your workloads need DNS resolution, you must add a separate NetworkPolicy that allows egress to your cluster's DNS service." This breaks `postgres-age.<ns>.svc.cluster.local` resolution and is the single most common way default-deny egress silently bricks a deployment.

Also critical for the multi-selector syntax: a single `from` entry with both `namespaceSelector` and `podSelector` means AND; two separate list entries mean OR. The docs spell this out with a worked example, and getting it wrong silently widens the policy.

### 3.2 Complete policy set for the Kora topology

```yaml
# 1. Default deny everything, both directions, namespace-wide.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kora-default-deny
  namespace: {{ .Release.Namespace }}
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
---
# 2. DNS egress for every pod. Without this, nothing resolves.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kora-allow-dns
  namespace: {{ .Release.Namespace }}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
---
# 3. API ingress: web UI, Prometheus, and the consolidation CronJob.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kora-api-ingress
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app: kora-api
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: kora-web
      ports:
        - protocol: TCP
          port: 8080
        - protocol: TCP
          port: 50051
    # Prometheus scrapes only the metrics port.
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
          podSelector:
            matchLabels:
              app.kubernetes.io/name: prometheus
      ports:
        - protocol: TCP
          port: 8080
---
# 4. API egress: Postgres only.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kora-api-egress
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app: kora-api
  policyTypes: [Egress]
  egress:
    - to:
        - podSelector:
            matchLabels:
              app: postgres-age
      ports:
        - protocol: TCP
          port: 5432
---
# 5. Postgres ingress: API and consolidation job only. No egress rule at all,
#    so rule 1 leaves it egress-isolated except for DNS from rule 2.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: postgres-ingress
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app: postgres-age
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: kora-api
        - podSelector:
            matchLabels:
              app: kora-consolidation
      ports:
        - protocol: TCP
          port: 5432
```

Notes on this set:

- `kubernetes.io/metadata.name` is set automatically on every namespace by the API server since Kubernetes 1.21 (NamespaceDefaultLabelName, GA in 1.22), so selecting `kube-system` and `monitoring` by that label needs no manual labelling (<https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/#automatic-labelling>).
- Policy 3's Prometheus rule uses the AND form (both selectors in one `from` entry). This is deliberate; splitting them would allow *any* pod in the monitoring namespace **or** any Prometheus-labelled pod cluster-wide.
- The consolidation CronJob must carry `app: kora-consolidation` for policy 5 to work. Worth verifying against `templates/consolidation.yaml`.
- Health probes come from the kubelet on the node, and the docs state that when a pod is isolated for ingress, "the only allowed connections into the pod are those from the pod's node and those allowed by the ingress list" - so probes survive default-deny.
- If the web UI needs to reach the API through a NodePort or ingress controller, the source IP may be rewritten. The docs warn that for ingress "the 'source IP' that the NetworkPolicy acts on may be the IP of a LoadBalancer or of the Pod's node," which can defeat pod-selector rules. Worth testing with the actual exposure method.

### 3.3 Enforcement: which CNI, and the kind caveat

**Creating a NetworkPolicy without an enforcing CNI is a no-op.** Kubernetes says so twice: "Creating a NetworkPolicy resource without a controller that implements it will have no effect," and "POSTing this to the API server for your cluster will have no effect unless your chosen networking solution supports network policy" (<https://kubernetes.io/docs/concepts/services-networking/network-policies/>). This is the dangerous case: `kubectl apply` succeeds, `kubectl get netpol` shows the policy, and nothing is enforced.

**kind specifically.** A kind maintainer stated on issue #3705: "NetworkPolicy isn't part of Kubernetes conformance and current kind releases did not implement NetworkPolicy at all... However, there will be a network policy implementation feature in the next release, which will happen around when Kubernetes 1.31 releases" (<https://github.com/kubernetes-sigs/kind/issues/3705>). Kindnet has since gained it: "Kindnet implements Kubernetes Network Policies using the kube-network-policies project" via first-packet inspection in user space, and also supports Admin Network Policies (<https://kindnet.sigs.k8s.io/docs/user/network-policies/>).

**Practical guidance:** on older kind, disable the default CNI and install Calico or Cilium:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: "192.168.0.0/16"   # Calico's default
```

Enforcing CNIs include Calico, Cilium, Antrea, Weave, and Kube-router. Notably **Flannel does not implement NetworkPolicy**, and neither does AWS VPC CNI on its own (EKS requires enabling the network policy feature or running Calico alongside). Inference: Kora should ship a `NOTES.txt` warning and, ideally, a CI e2e test that *asserts a denied connection actually fails* - because a policy that renders but does not enforce is worse than no policy, since it creates false confidence.

---

## 4. RBAC and ServiceAccount

Kora's API talks to Postgres. It makes **no** Kubernetes API calls. So the correct answer is: a dedicated ServiceAccount with no RoleBinding at all, and no mounted token.

**Confirmed.** The Kubernetes docs: "If you don't want the kubelet to automatically mount a ServiceAccount's API credentials, you can opt out of the default behavior. You can opt out of automounting API credentials on `/var/run/secrets/kubernetes.io/serviceaccount/token` for a service account by setting `automountServiceAccountToken: false`." It can be set on the ServiceAccount or on the Pod, and "If both the ServiceAccount and the Pod's `.spec` specify a value for `automountServiceAccountToken`, the Pod spec takes precedence." (<https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/>)

Also relevant: if you specify nothing, "Kubernetes automatically assigns the ServiceAccount named `default` in that namespace." Kora's pods currently use `default` with an auto-mounted token. That token is a real credential - it can at minimum call `/api/v1/...` self-discovery endpoints, and its blast radius grows with any cluster-wide RoleBinding an operator later adds to `system:serviceaccounts`.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kora-api
  namespace: {{ .Release.Namespace }}
automountServiceAccountToken: false
---
# in the Deployment podSpec:
spec:
  serviceAccountName: kora-api
  automountServiceAccountToken: false   # belt and braces; pod spec wins
```

Set both. The redundancy costs nothing and survives someone later flipping the ServiceAccount field.

**No Role or RoleBinding should be created.** RBAC good practices state the principle directly - grant least privilege, and Kubernetes documents that "Restrict `watch` or `list` access to only the most privileged, system-level components" (<https://kubernetes.io/docs/concepts/security/rbac-good-practices/>, <https://kubernetes.io/docs/concepts/security/secrets-good-practices/>). A service with zero API needs gets zero permissions. Shipping an empty Role "for completeness" is an anti-pattern - it becomes the place someone adds a permission later.

One nuance: a dedicated ServiceAccount is still worth creating even with no permissions, because it gives the workload a distinct identity for audit logs, for `imagePullSecrets`, and for future workload-identity federation (IRSA, GKE Workload Identity) if the operator ever wants the pod to reach a cloud secret manager directly.

---

## 5. TLS

### 5.1 Postgres: `sslmode=disable` is the wrong default

Postgres documents the modes precisely: "By default, PostgreSQL will not perform any verification of the server certificate. This means that it is possible to spoof the server identity (for example by modifying a DNS record or by taking over the server IP address) without the client knowing." With `verify-ca`, libpq "will verify that the server is trustworthy by checking the certificate chain up to the root certificate stored on the client." With `verify-full`, it "will also verify that the server host name matches the name stored in the server certificate." And: **"verify-full is recommended in most security-sensitive environments."** (<https://www.postgresql.org/docs/current/libpq-ssl.html>)

The mode hierarchy matters because two of them are security theater:

| Mode | Eavesdropping | MITM | Notes |
|---|---|---|---|
| `disable` | No protection | No protection | Current setting |
| `require` | Encrypted | **No protection** | Encrypts, but accepts any certificate |
| `verify-ca` | Encrypted | Protected against a non-CA-signed attacker | Does not check hostname |
| `verify-full` | Encrypted | Protected | Recommended |

The gap between `require` and `verify-full` is the entire point: `require` will happily complete a TLS handshake with an attacker who has hijacked the Service IP or poisoned CoreDNS. Inference: if Kora changes only one character here, changing `disable` to `require` is a *marginal* improvement that mostly buys compliance-checkbox encryption; `verify-full` with a mounted CA is the change that actually removes the in-cluster MITM.

Why this matters in-cluster at all, given a NetworkPolicy: pod-to-pod traffic crosses the node network unencrypted. Anyone with node-level access, a compromised sidecar in the same pod network, or the ability to run a privileged pod can capture it. For a memory engine, that traffic *is* the user's data - every stored memory, every query, every embedding. NetworkPolicy limits who can *connect*; it does not stop passive capture on the wire.

`pgx` honors standard libpq parameters, so the DSN becomes:

```
postgres://user@host:5432/db?sslmode=verify-full&sslrootcert=/etc/ssl/pg/ca.crt
```

Certificate provisioning: cert-manager with a self-signed in-cluster Issuer is the standard answer, and it handles rotation (<https://cert-manager.io/docs/configuration/selfsigned/>). Inference: make it opt-in (`postgres.tls.enabled`) defaulting to on for production values, since a self-hosted kind user without cert-manager needs an escape hatch.

### 5.2 mTLS: service mesh vs application-level

**Service mesh (Istio, Linkerd).** Linkerd enables mTLS between meshed pods by default with no application changes (<https://linkerd.io/2/features/automatic-mtls/>); Istio does the same and can enforce `PeerAuthentication` in STRICT mode (<https://istio.io/latest/docs/concepts/security/#mutual-tls-authentication>). Both give automatic certificate rotation, workload identity via SPIFFE, and encryption for *all* traffic including the web-to-API hop, without touching Go code.

Cost: a mesh is a substantial operational dependency - control plane, sidecar or ambient dataplane, per-pod resource overhead, upgrade coordination, and a new class of failure modes. It also does not help the Postgres hop unless Postgres is meshed too, and meshing a StatefulSet database is its own project.

**Application-level TLS.** Go's `crypto/tls` with `ClientAuth: tls.RequireAndVerifyClientCert` on the gRPC server, plus `sslmode=verify-full` to Postgres. Full control, no new infrastructure, works identically on kind and EKS. Cost: Kora owns certificate distribution, rotation, and revocation - the parts meshes exist to solve.

**Recommendation for this project (inference).** Kora is an OSS component that people self-host into *someone else's* cluster. It should not have an opinion about the operator's mesh. The right posture is:

1. **Do** implement application-level TLS to Postgres (`verify-full`), because Kora owns that connection and no mesh will cover it by default.
2. **Do** support serving TLS on the gRPC/HTTP listeners when certs are supplied, so a non-mesh operator can secure the north-south hop.
3. **Do not** require or bundle a mesh. Instead, be *mesh-compatible*: use named ports with protocol prefixes (`grpc`, `http`) so Istio can classify traffic, avoid binding to `0.0.0.0` assumptions that break sidecar interception, and document `PeerAuthentication` STRICT as a supported deployment.
4. **Do** document that mTLS via a mesh is the recommended production posture for the east-west hops, and that Kora does not need to reimplement it.

That is: own what you own, be compatible with what you do not.

---

## 6. Pod Security Standards - restricted profile

### 6.1 What restricted requires, checked line by line

From the Pod Security Standards page (<https://kubernetes.io/docs/concepts/security/pod-security-standards/>), Restricted = everything in Baseline plus:

| Restricted control | Required value | Kora `api.yaml` | Status |
|---|---|---|---|
| Privilege escalation | `allowPrivilegeEscalation: false` | line 57 | Pass |
| Running as non-root | `runAsNonRoot: true` | line 47 | Pass |
| Non-root user (v1.23+) | `runAsUser` != 0 | `1000`, line 48 | Pass |
| Seccomp (v1.19+) | `RuntimeDefault` or `Localhost`, explicitly set | lines 50-51 | Pass |
| Capabilities (v1.22+) | must `drop: ["ALL"]`; may only add `NET_BIND_SERVICE` | lines 59-60 | Pass |
| Volume types | only configMap, csi, downwardAPI, emptyDir, ephemeral, persistentVolumeClaim, projected, secret | `emptyDir` only | Pass |
| Baseline: host namespaces | `hostNetwork`/`hostPID`/`hostIPC` unset or false | unset | Pass |
| Baseline: privileged | unset or false | unset | Pass |
| Baseline: hostPath | forbidden | none | Pass |
| Baseline: hostPort | unset or 0 | `containerPort` only, lines 62-65 | Pass |
| Baseline: `/proc` mount | unset or `Default` | unset | Pass |
| Baseline: sysctls | unset or safe subset | unset | Pass |
| Baseline: host in probes/hooks (v1.34+) | unset or `""` | probes use `port`, not `host` | Pass |

**Answer: yes, the API pod spec already satisfies restricted.** That is a genuinely good baseline and better than most charts.

Three caveats:

- `readOnlyRootFilesystem: true` (line 58) is **not** a PSS requirement at any level. It is stronger than restricted requires. Good, but do not assume PSS covers it.
- The seccomp profile is set at pod level (line 50), which the docs permit: "The container fields may be undefined/nil if the pod-level `spec.securityContext.seccompProfile.type` field is set appropriately." Same for `runAsNonRoot`.
- **The other three workloads were not audited here.** The Postgres StatefulSet, the web Deployment, and the consolidation CronJob all need the same treatment, and PSS is evaluated per pod - one non-compliant pod is rejected on admission. Postgres in particular commonly needs `fsGroup` and may resist `readOnlyRootFilesystem`. Inference: verify these before enabling `enforce: restricted`, or the chart will fail to install.

### 6.2 Namespace labels

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kora
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: v1.33
    pod-security.kubernetes.io/audit: restricted
    pod-security.kubernetes.io/audit-version: v1.33
    pod-security.kubernetes.io/warn: restricted
    pod-security.kubernetes.io/warn-version: v1.33
```

Pin the version. The docs' own example pins it, and the reason is that an unpinned label silently adopts new controls as the cluster upgrades - the v1.34 host-in-probes control is exactly such an addition. Pin to a version at or below the oldest cluster you support.

To evaluate safely before enforcing, the docs give the dry-run path: `kubectl label --dry-run=server --overwrite ns --all pod-security.kubernetes.io/enforce=baseline` - "The Pod Security Standard checks will still be run in dry run mode, giving you information about how the new policy would treat existing pods, without actually updating a policy." (<https://kubernetes.io/docs/tasks/configure-pod-container/enforce-standards-namespace-labels/>)

Pod Security Admission is GA from Kubernetes 1.25 (same source). Inference: Helm creates namespaces without labels when using `--create-namespace`, so Kora should either template a Namespace object (guarded by a value) or document the `kubectl label` command in `NOTES.txt`.

---

## 7. Supply chain

### 7.1 What Kora already has

`.github/workflows/security.yaml` runs gosec with SARIF upload, Trivy against both images (CRITICAL/HIGH) with SARIF upload, and `dependency-review-action` with `fail-on-severity: high`, on PRs and weekly. That is already above the median OSS project. Two weaknesses: the actions are pinned to `@master` (`securego/gosec@master`, `aquasecurity/trivy-action@master`), which is itself a supply-chain risk - a compromised upstream tag executes in a workflow with `security-events: write`. Pin to a commit SHA. And the scan builds throwaway `:scan` tags rather than scanning the artifact that is actually released.

### 7.2 What is missing

**SBOM.** Syft "generates SBOMs for container images, filesystems, archives," supports "CycloneDX, SPDX, Syft JSON, and more," and can "create signed SBOM attestations using the in-toto specification" (<https://github.com/anchore/syft>). Trivy also emits SBOMs natively. Note that Docker Buildx can produce SBOM and provenance attestations inline with `--attest type=sbom --attest type=provenance` (<https://docs.docker.com/build/attestations/>), which is often the lowest-friction path since the project already builds with Docker.

**Signing.** Cosign keyless signing binds an OIDC identity rather than a key: "Fulcio issues short-lived certificates binding an ephemeral key to an OpenID Connect identity. Signing events are logged in Rekor, a signature transparency log, providing an auditable record of when a signature was created." And: "For security, the private key is destroyed shortly after and the short-lived identity certificate expires." (<https://docs.sigstore.dev/cosign/signing/overview/>) For a GitHub-Actions-built OSS project this is close to free - no key to store, no key to leak, no key to rotate.

**Provenance.** SLSA Build L1 requires only that "Provenance exists describing how the artifact was built, including the build platform, build process, and top-level inputs" and that the producer distributes it. L2 adds that builds run "on a hosted platform that generates and signs the provenance" and is "tied to that infrastructure through a digital signature." (<https://slsa.dev/spec/v1.0/levels>) GitHub Actions with `actions/attest-build-provenance` reaches L2 essentially out of the box.

### 7.3 Is this table stakes?

**For an OSS project claiming enterprise readiness: yes, and the bar is low.** The specific argument: enterprise procurement and vendor-security review increasingly asks for an SBOM by name, driven by US Executive Order 14028 and the resulting NIST Secure Software Development Framework (SSDF, NIST SP 800-218), which calls for archiving and protecting each release and providing provenance data (<https://csrc.nist.gov/pubs/sp/800/218/final>). The EU Cyber Resilience Act entered into force in December 2024 with main obligations applying from December 2027, and it imposes SBOM and vulnerability-handling requirements on products with digital elements placed on the EU market (<https://eur-lex.europa.eu/eli/reg/2024/2847/oj>). Self-hosted OSS used inside an enterprise gets pulled into these reviews.

Concretely, a release job that adds SBOM + cosign + provenance is roughly 25 lines:

```yaml
permissions:
  contents: read
  packages: write
  id-token: write        # required for keyless signing
  attestations: write

- uses: sigstore/cosign-installer@v3
- uses: anchore/sbom-action@v0
  with:
    image: ghcr.io/kora/kora:${{ github.ref_name }}
    format: spdx-json
    upload-artifact: true
- run: cosign sign --yes ghcr.io/kora/kora@${{ steps.build.outputs.digest }}
- run: cosign attest --yes --predicate sbom.spdx.json --type spdxjson \
         ghcr.io/kora/kora@${{ steps.build.outputs.digest }}
- uses: actions/attest-build-provenance@v1
  with:
    subject-name: ghcr.io/kora/kora
    subject-digest: ${{ steps.build.outputs.digest }}
    push-to-registry: true
```

Sign the **digest**, not the tag - tags are mutable and signing one signs nothing durable.

Inference on priority: this is high-value-per-hour but it is not what protects users *today*. A signed image with a hardcoded API key in its chart is still trivially compromised. Supply chain belongs after the credential work, which is why it is #5 and not #1 in the ranking below.

---

## 8. Audit logging

### 8.1 What to log

OWASP's Logging Cheat Sheet lists the security use cases directly: "Identifying security incidents, Monitoring policy violations, Assisting non-repudiation controls, Audit trails e.g. data addition, modification and deletion, data exports, Compliance monitoring" (<https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html>). It also warns to "not log too much, or too little" and notes that audit trails and security event logs are collected for different purposes and "should be kept separate."

For a memory engine, the events that matter:

| Event | Fields |
|---|---|
| Auth success | timestamp, key **id** (never the secret), project_id, source IP, method, user agent |
| Auth failure | timestamp, key id if parseable, source IP, reason class only |
| Rate limit exceeded | timestamp, key id, endpoint |
| Memory created | timestamp, key id, project_id, memory_id, byte size - **never content** |
| Memory read/searched | timestamp, key id, project_id, result count, query **hash** - never the query text |
| Memory updated/deleted | timestamp, key id, project_id, memory_id, before/after **hashes** |
| Bulk export | timestamp, key id, project_id, record count - highest-value event for SOC 2 |
| Consolidation job run | start/end, records pruned/decayed, counts only |
| API key lifecycle | created, rotated, revoked, by whom |
| Config change | what changed, by whom - never the values |
| Cross-project access attempt | any request where project_id does not match the key's project |

The last row is the one specific to Kora's threat model. `project_id` is currently just a string filter, which means tenant isolation rests entirely on the correctness of every query's WHERE clause. Logging attempted cross-project access turns a silent isolation bug into a detectable event. Inference: this is the highest-value single log line in the list.

SOC 2 relevance: the Trust Services Criteria CC7.2 requires monitoring for anomalies and CC6.1 covers logical access controls (<https://www.aicpa-cima.com/resources/download/2017-trust-services-criteria-with-revised-points-of-focus-2022>). Auditors ask "who accessed what data, when" and "how would you know if someone exfiltrated everything." The bulk-export and cross-project rows answer both.

### 8.2 What must never be logged

**Never, under any circumstance:**

- **Memory content, in whole or in part.** This is the user's data and the entire reason the product exists. It must not appear in logs, error messages, stack traces, or spans.
- **Search query text.** Queries against a memory store are as sensitive as the memories - "what did I say about my medical diagnosis" is itself the disclosure.
- **Embedding vectors.** These are not anonymous. Embedding-inversion research demonstrates that substantial portions of input text can be reconstructed from dense embeddings; see Morris et al., "Text Embeddings Reveal (Almost) As Much As Text" (EMNLP 2023, <https://arxiv.org/abs/2310.06816>), which recovers 92% of 32-token inputs exactly. Treating vectors as non-sensitive is a mistake.
- **API keys, or any prefix long enough to be useful.** Log the key *id*, never the secret half.
- **Database DSNs, passwords, connection strings.** These leak constantly through connection-error messages - `pgx` errors can embed the DSN. Wrap and scrub.
- **Full request/response bodies.** The single most common way content ends up in logs is a debug-level body dump left enabled.
- **PII in free-text fields** - names, emails, identifiers inside metadata blobs.

OWASP's cheat sheet lists excluded data explicitly, including "Application source code, Session identification values, Access tokens, Sensitive personal data, Passwords, Database connection strings, Encryption keys and other primary secrets" (<https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html>).

**Design consequence.** Do not rely on redaction filters - they are best-effort and fail open. Structure the logger so content *cannot* reach it: pass IDs and sizes into the audit path, never the memory struct. A `LogValuer`/`MarshalJSON` on the memory type that returns only `{id, size, project_id}` makes the safe thing automatic and the unsafe thing require deliberate effort. Go's `log/slog` supports exactly this via the `slog.LogValuer` interface (<https://pkg.go.dev/log/slog#LogValuer>).

Also: audit logs should be append-only, shipped off-node (a pod's logs die with the pod), and retained per policy - SOC 2 engagements commonly expect one year. GDPR Article 5(1)(e) storage limitation applies to the audit log too, so "retain forever" is not a safe default (<https://eur-lex.europa.eu/eli/reg/2016/679/oj>).

---

## 9. Top 5 priorities

Ranked by (damage prevented) x (likelihood of occurring in a real self-hosted install) / (effort). The weighting reflects that Kora is OSS software installed by people who will not read the docs.

### 1. Remove all default credentials from the chart, and stop inlining the DB password

**Why first.** Everything else is defense in depth around a door that is currently propped open. `ctx0_dev_key_1` is published in a public GitHub repo; anyone who installs the chart unchanged and exposes it - and the chart offers NodePort on 30000 and 30080 to make that easy - has a memory store readable by anyone who has read the README. The Postgres password at `api.yaml:68` is not even in a Secret; it is in the Deployment spec, visible to anyone with `get deployments`.

**Concretely:** empty defaults; `fail` with a helpful message if unset; `existingSecret` support for both credentials; `secretKeyRef` for the DSN. Small, mechanical, and it eliminates the only finding here that is *remotely exploitable with zero attacker skill*.

### 2. Hash API keys, fix the auth bypass, add constant-time comparison

**Why second.** This is the durable fix behind #1. Prefixed keys (`ctx0_live_<id>_<secret>`), SHA-256 at rest, `subtle.ConstantTimeCompare`, per-key identity so keys can be revoked individually and attributed in audit logs. Bundle the `!strings.HasPrefix(path, "/v1/")` inversion into this change - a deny-by-default middleware with an explicit exemption list. Arguably that bypass is the more serious of the two, since it silently unauthenticates every future non-`/v1/` route.

Explicitly **not** bcrypt or Argon2 - see §1.3. The keys are checked on every request inside a 500m CPU budget.

### 3. NetworkPolicy (default-deny) + `automountServiceAccountToken: false`

**Why third.** These are pure-YAML, zero-code, zero-risk-to-application-behavior changes that shrink the blast radius of any future application vulnerability. If the API is ever RCE'd, default-deny egress means the attacker cannot reach the cluster's other workloads or exfiltrate to the internet, and no ServiceAccount token means no pivot to the Kubernetes API.

Ship with the CNI caveat prominently documented and, ideally, a CI test that asserts a denied connection *actually fails* - because an unenforced policy is worse than none.

### 4. TLS to Postgres (`sslmode=verify-full`) + audit logging

**Why fourth.** Both are about the data itself rather than the perimeter. `sslmode=disable` means every memory, query, and embedding crosses the node network in cleartext. Audit logging with the never-log rules from §8 is what makes an incident *investigable* and what a SOC 2 auditor actually asks to see. The cross-project-access-attempt log line is the one that would catch a tenant-isolation bug in a `project_id` filter.

These are grouped because both require Go changes and both directly concern user data confidentiality.

### 5. Supply chain: SBOM, cosign signing, SLSA provenance

**Why fifth, not lower.** The existing gosec/Trivy/dependency-review setup already covers the *finding vulnerabilities* half. Adding SBOM + keyless signing + provenance attestation is roughly 25 lines of workflow YAML and directly answers the questions that appear in enterprise vendor-security questionnaires. Pin the currently-`@master` actions to SHAs while you are in there.

**Why not higher:** it protects the *distribution* of the software, and nothing in the distribution chain is currently known to be compromised. The chart's default credentials are compromised by publication, today.

### Deliberately below the line

- **Distributed rate limiting.** The in-process token bucket is documented as per-replica and the chart says so honestly. It is a capacity concern, not a security boundary. An ingress-level or mesh-level limit is the standard answer and belongs to the operator.
- **Service mesh / mTLS everywhere.** Correct destination, wrong dependency for an OSS component to impose. Be mesh-compatible; do not bundle one. See §5.2.
- **Full multi-tenancy isolation** (row-level security, per-tenant schemas, per-tenant encryption keys). Real work, real value, but a design project rather than a hardening pass. The cheap interim step - logging cross-project access attempts - is folded into #4.
- **Pod Security Standards labels.** Already satisfied by the API pod (§6.1); adding the namespace labels is a one-line documentation change, worth doing but not a change in posture. Audit the Postgres, web, and consolidation pods first.

---

## Appendix: sources

**Kubernetes (primary)**
- Network Policies - <https://kubernetes.io/docs/concepts/services-networking/network-policies/>
- Pod Security Standards - <https://kubernetes.io/docs/concepts/security/pod-security-standards/>
- Enforce PSS with Namespace Labels - <https://kubernetes.io/docs/tasks/configure-pod-container/enforce-standards-namespace-labels/>
- Configure Service Accounts for Pods - <https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/>
- Secrets - <https://kubernetes.io/docs/concepts/configuration/secret/>
- Good practices for Kubernetes Secrets - <https://kubernetes.io/docs/concepts/security/secrets-good-practices/>
- RBAC good practices - <https://kubernetes.io/docs/concepts/security/rbac-good-practices/>
- Namespaces (automatic labelling) - <https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/#automatic-labelling>

**Go / crypto**
- crypto/subtle - <https://pkg.go.dev/crypto/subtle>
- log/slog LogValuer - <https://pkg.go.dev/log/slog#LogValuer>
- RFC 9106 (Argon2) - <https://www.rfc-editor.org/rfc/rfc9106.html>

**OWASP / NIST**
- Logging Cheat Sheet - <https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html>
- Secrets Management Cheat Sheet - <https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html>
- Password Storage Cheat Sheet - <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>
- Authorization Cheat Sheet - <https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html>
- NIST SP 800-63B - <https://pages.nist.gov/800-63-3/sp800-63b.html>
- NIST SP 800-218 (SSDF) - <https://csrc.nist.gov/pubs/sp/800/218/final>

**Secrets tooling**
- External Secrets Operator - <https://external-secrets.io/latest/introduction/overview/>
- Sealed Secrets - <https://github.com/bitnami-labs/sealed-secrets>
- SOPS - <https://github.com/getsops/sops>
- Secrets Store CSI Driver - <https://secrets-store-csi-driver.sigs.k8s.io/>
- Helm `fail` - <https://helm.sh/docs/chart_template_guide/function_list/#fail>
- Helm `lookup` - <https://helm.sh/docs/chart_template_guide/functions_and_pipelines/#using-the-lookup-function>

**Supply chain**
- Sigstore cosign - <https://docs.sigstore.dev/cosign/signing/overview/>
- Syft - <https://github.com/anchore/syft>
- Trivy - <https://trivy.dev/latest/docs/>
- SLSA levels - <https://slsa.dev/spec/v1.0/levels>
- Docker build attestations - <https://docs.docker.com/build/attestations/>
- EU Cyber Resilience Act - <https://eur-lex.europa.eu/eli/reg/2024/2847/oj>

**Networking / TLS / mesh**
- kind known issues - <https://kind.sigs.k8s.io/docs/user/known-issues/>
- kind NetworkPolicy issue #3705 - <https://github.com/kubernetes-sigs/kind/issues/3705>
- Kindnet Network Policies - <https://kindnet.sigs.k8s.io/docs/user/network-policies/>
- PostgreSQL SSL Support - <https://www.postgresql.org/docs/current/libpq-ssl.html>
- cert-manager self-signed issuer - <https://cert-manager.io/docs/configuration/selfsigned/>
- Linkerd automatic mTLS - <https://linkerd.io/2/features/automatic-mtls/>
- Istio mutual TLS - <https://istio.io/latest/docs/concepts/security/>

**Other**
- GitHub token formats - <https://github.blog/engineering/platform-security/behind-githubs-new-authentication-token-formats/>
- Morris et al., Text Embeddings Reveal (Almost) As Much As Text - <https://arxiv.org/abs/2310.06816>
- AICPA Trust Services Criteria - <https://www.aicpa-cima.com/resources/download/2017-trust-services-criteria-with-revised-points-of-focus-2022>
