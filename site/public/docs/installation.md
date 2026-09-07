# Installation

kora is designed to run in Kubernetes. The Helm chart is the supported path; Docker Compose is for trying it out, and is covered in the [Quick start](quickstart.md).

## Prerequisites

- A Kubernetes cluster, or [kind](https://kind.sigs.k8s.io/) for local work
- [Helm](https://helm.sh/) 3.x
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

## Credentials first

The chart ships no default password and no default API key, and refuses to install without them. This is deliberate: a default credential in a public chart is a published credential, identical across every install that never changed it.

Generate a key offline. The server stores only its hash, so this printed value is the only copy:

```bash
go run ./cmd/cli keys generate
```

## Install

```bash
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.password="$(openssl rand -base64 24 | tr -d '/+=')" \
  --set auth.apiKeys="<the key printed above>"
```

For anything past a local trial, point the chart at Secrets you manage yourself, with External Secrets, Sealed Secrets, SOPS, or a CSI driver. When `existingSecret` is set the chart creates no Secret of its own:

```bash
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.existingSecret=my-postgres-secret \
  --set auth.existingSecret=my-api-keys
```

> Passing more than one API key with `--set` does not work as you would expect: Helm reads `,` as a list separator. Use `--set-string auth.apiKeys="key1\,key2"`, or better, a values file.

## Harden the namespace

Helm does not manage the namespace it installs into: `--create-namespace` makes a bare one. Pod Security Standards have to be applied to it directly:

```bash
kubectl label namespace kora \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```

Every workload in the chart satisfies `restricted`. Labelling the namespace is what makes a future regression fail admission rather than quietly run privileged.

## Network policy

The chart ships a default-deny NetworkPolicy for the namespace, opening only the flows the system needs. It is on by default (`networkPolicy.enabled=true`).

> **Check your CNI.** Enforcement is the CNI's job, not Kubernetes'. kind's default kindnet ignores NetworkPolicy entirely, so these objects apply cleanly there and do nothing at all. Calico, Cilium, and the major managed CNIs enforce them. The resource existing is not evidence it is being enforced.

This closed a real hole: before it, a pod in an unrelated namespace could connect straight to Postgres and read every memory row, bypassing the API's authentication and rate limiting.

If the API needs to reach something outside the cluster - a hosted embedding provider, an OTLP collector, a managed Postgres - add rules with `networkPolicy.apiExtraEgress`, which takes standard NetworkPolicy egress syntax.

## Local cluster with kind

```bash
make k8s-setup
```

`make k8s-setup` generates a password and API key into `.dev-credentials` (gitignored) on first run, and reuses them afterwards.
The same command applies pod security hardening to the `kora` namespace and deploys with `charts/kora/values-kind.yaml`.
You can override files and flags with env vars such as `K8S_SETUP_HELM_VALUES_FILE` and `K8S_SETUP_HELM_EXTRA_FLAGS` when needed.
Use `make k8s-smoke-check` if you want a quick setup+write-path smoke check.
For quick checks: `make k8s-status`, `make k8s-verify`, and if needed `make k8s-logs`.

Run the end-to-end tests against it:

```bash
. ./.dev-credentials
KORA_E2E_HTTP=http://localhost:8080 \
KORA_E2E_API_KEY="$DEV_API_KEY" \
go test ./test/e2e/... -v -tags=e2e
```

Tear it down with `make k8s-teardown`.

## Exposure

The API Service is `ClusterIP` by default. For kind, NodePort is available:

```yaml
service:
  type: ClusterIP
  nodePort:
    enabled: true
    grpc: 30051
    http: 30080
```

Keep `/metrics` and `/v1/health` inside the cluster. Neither requires an API key, and health reports graph totals.

## What gets installed

| Workload | Purpose | Optional |
| --- | --- | --- |
| API server | gRPC on 50051, REST and metrics on 8080 | no |
| PostgreSQL | With Apache AGE and pgvector | no |
| Consolidation CronJob | Decay, pruning, graph hygiene | `consolidation.enabled` |
| Web UI | Graph visualisation, NodePort 30000 | `web.enabled` |

Each workload gets its own ServiceAccount, none of which can reach the Kubernetes API, and none of which mount a token. None of these workloads call the Kubernetes API at all.

## Next

- [Configuration](configuration.md) - the values worth changing, and the ones sized from measurement
- [Operations](operations.md) - metrics, consolidation, and backups
