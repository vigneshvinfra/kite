# Security & Best Practices

How this repo's production deploy is secured, organized by the four
asks in the assignment, plus known gaps.

### Secret Management

We use the Secrets Store CSI Driver, which lets us keep secrets in a
central place like AWS Secrets Manager. The CSI driver synchronizes
secrets from external APIs and mounts them into pods as volumes (or
syncs them to Kubernetes Secrets for env-var consumption).

#### Approach

Terraform creates the secret container, IAM role, and Pod Identity
association. The secret value itself is set by the user via the AWS
CLI, so the value never enters Terraform state or git:

```bash
aws secretsmanager put-secret-value --secret-id prod/myapp/api-key --secret-string "<actual-key>"
```

Pod Identity gives the pod its own AWS identity, scoped to exactly
one ServiceAccount in one namespace on one cluster:

```hcl
resource "aws_iam_role" "myapp" {
  name_prefix = "myapp-prod-"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

resource "aws_eks_pod_identity_association" "myapp" {
  cluster_name    = module.eks.cluster_name
  namespace       = "myapp-prod"
  service_account = "myapp"
  role_arn        = aws_iam_role.myapp.arn
}
```

The IAM policy attached to that role lists the two specific secret ARNs only no wildcards, no `secretsmanager:*`:

```hcl
data "aws_iam_policy_document" "myapp_secrets" {
  statement {
    effect = "Allow"
    actions = [
      "secretsmanager:GetSecretValue",
      "secretsmanager:DescribeSecret",
    ]
    resources = [
      aws_secretsmanager_secret.myapp_api_key.arn,
      aws_secretsmanager_secret.myapp_config.arn,
    ]
  }
}
```

Rationale: a compromised pod can read *those two* secrets and nothing
else in the AWS account.

At runtime, the AWS CSI provider uses Pod Identity to mint AWS
credentials for the `myapp-prod/myapp` ServiceAccount (the agent on
the node knows the association → role mapping). With those
credentials, the provider calls `secretsmanager:GetSecretValue` for
both secret ARNs. It writes `config.json` to the CSI volume —
visible to the pod at `/mnt/secrets-store/config.json` — and
materializes a `Secret/myapp-env` containing the `API_KEY` value.

#### Advantages

- No plaintext secret values in any values files.
- No `.env` files.
- No secrets committed via debug logs or stack traces.
- No IAM keys anywhere — credentials are ephemeral, minted by Pod
  Identity at runtime.


### Access control (RBAC)
We have two ways to enforce boundaries.
- Argo CD project boundaries (who can deploy what).
- Kubernetes RBAC (what each workload can do once running).

**Argo CD AppProjects**  are defined in `argocd/projects/*/`. This would constrain each Application to a narrow scope:
The Default ArgoCD project is fully permissive by default and it allows unrestricted app deployment from any git source and it can target any cluster or namespace. The projects also pin `sourceRepos` so an Application can only
fetch from `git@github.com:vigneshvinfra/kite.git` plus the
upstream Helm repos it's expected to use.

| Project | Destinations | Cluster-scoped resources allowed |
|---|---|---|
| `myapp-prod` | `prod(myapp-prod)`  | `Namespace`  |
| `infra-prod` | `prod(ingress-nginx)`  | `Namespace`, `ClusterRole`, `ClusterRoleBinding`, `IngressClass`, `ValidatingWebhookConfiguration` |
| `monitoring-prod` | `prod(monitoring` + `kube-system)` | `Namespace`, `ClusterRole`/`Binding`, `CRDs`, `validating/mutating webhooks` |
| `secrets-store-csi-prod` | `prod(kube-system)` | `Namespace`, `ClusterRole`/`Binding`, `CSIDriver`, `CRDs` |

A buggy or malicious change to the `myapp` Helm chart can affect
only the `myapp-prod` namespace on the prod cluster. It cannot
install a `ClusterRoleBinding`, target another namespace, or
deploy to a different cluster. Argo rejects the sync with a
clear "destination not permitted" error.

note: ArgoCD human RBAC is for now local-admin only. We would need to add OIDC/SAML auth and map groups to project actions via config map.


**Kubernetes RBAC for the workload itself:** `myapp` has no
ClusterRole or Role. (`rbac.create: false` in `values.yaml`). The app doesn't call the Kubernetes API. The myapp ServiceAccount also sets `automountServiceAccountToken: false`, so the pod never receives a Kubernetes API token in the first place. Even if the app process is compromised, there's no SA token file on disk to steal, an attacker
has nothing to authenticate to the K8s API with.


### Network isolation
The cluster runs in a VPC with three subnet tiers. Worker nodes have **no public IPs**. They only have private IPs from the private subnets. Outbound internet (when needed) goes through a single NAT gateway. Inbound traffic is *never* directed at nodes directly — only at load balancers in the public subnets.

#### NetworkPolicy
Kubernetes' default network policy is **default-allow** which means any pod can talk to any other in any namespace.

default-deny:
This network policy elects myapp pods and declares both Ingress and Egress policy types with no rules. By itself Everything is denied i.e The pod can't be reached from another namespace, can't call out to the internet, can't reach internal services it's not explicitly allowed to., combined with the allow policies below, it makes the default posture explicit and visible in audits



#### NetworkPolicies —  Reference

All policies below are in addition to the explicit `<release>-default-deny`, which selects the same pods and declares both `Ingress` and `Egress` policy types. The default posture for `myapp` pods is therefore deny-all; the policies below are the explicit allows.

| Policy | Direction | Allows | Purpose | Rendered when |
|---|---|---|---|---|
| `<release>-allow-from-ingress` | Ingress | TCP 8000 from pods in `networkPolicy.ingressNamespace` | Lets the ingress controller forward HTTP requests into myapp pods. Without this rule, the app is unreachable from outside the cluster. | Always (when `networkPolicy.enabled`) |
| `<release>-allow-from-prometheus` | Ingress | TCP 8000 from any pod in `networkPolicy.monitoringNamespace` | Lets Prometheus pull metrics by making HTTP requests to myapp's `/metrics` endpoint. Without this rule, monitoring dashboards and alerts for myapp go blank. | `metrics.serviceMonitor.enabled: true` |
| `<release>-allow-egress-dns` | Egress | UDP 53 and TCP 53 to pods labelled `k8s-app: kube-dns` in `kube-system` | Name resolution via kube-dns / CoreDNS. Without this rule, any outbound connection that uses a hostname fails — including all the egress rules below. | Always (when `networkPolicy.enabled`) |
| `<release>-allow-egress-otel` | Egress | TCP `networkPolicy.otelCollector.port` to any pod in `networkPolicy.otelCollector.namespace` | Lets myapp send distributed traces (per-request timing and span data used for debugging slow requests) to the OpenTelemetry collector. Without this rule, traces stop appearing in your observability backend. | `networkPolicy.otelCollector.enabled: true` |
| `<release>-allow-egress-https` | Egress | TCP 443 to `0.0.0.0/0` **except** `169.254.169.254/32` (IMDS) and RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) | Lets myapp call third-party APIs on the public internet over HTTPS. The exclusions are the security-sensitive part: the IMDS exclusion stops a compromised pod from reading the EC2 node's AWS credentials | `networkPolicy.allowHTTPSEgress: true` |

### Image security
`Dockerfile` is a two-stage build:

- **Build stage** uses `golang:1.26-alpine` to compile a static
  binary (`CGO_ENABLED=0`). All toolchain and source stay in this
  stage.
- **Runtime stage** is `gcr.io/distroless/static-debian12:nonroot`.This doesnt have any shell, no package manager, no `curl`/`wget`. An attacker who lands an RCE has no tools to live off the land; they have to drop a
  binary, which is more visible.
- **Runs as UID 65532 (`nonroot`).** Most container-escape exploits
  assume root inside the container; nonroot closes the easiest paths.

The runtime image is roughly 10 MB. Less code, less attack surface.

- **Distroless runtime base** `gcr.io/distroless/static-debian12`
  contains only what's needed to run a static binary: ca-certs, glibc
  alternatives, tzdata. No shell, no package manager, no `curl`,
  `wget`, `nc`, `bash`. An attacker who lands a shell-style Remote Code Execution has nothing to really do.


- **`nonroot` user** The container's `USER` directive
  forces processes to run as a non-root UID. Running nonroot doesn't make escapes impossible, but it removes the easiest paths. An attacker can't modify system binaries, install packages, or write to most filesystem locations. Combined with a read-only root filesystem (readOnlyRootFilesystem: true), they're left with very little to work with. Can't read files owned by root that weren't explicitly made world-readable e.g. secrets mounted with restrictive perms.


- **Static binary, no CGO.** `CGO_ENABLED=0` produces a single
  statically-linked Go binary with no glibc dependency. The runtime
  image has no dynamic linker, no shared libraries, no toolchain.

- **Build Pipeline:** images are built and pushed only from the
  CI workflow in `.github/workflows/docker-publish.yaml`. No
  human builds and pushes from a laptop. We also have no build secrets baked in.
- **Vulnerability scanning:** Trivy (`aquasecurity/trivy-action`) scans every built image. SARIF output is uploaded to GitHub's Security tab via
  `github/codeql-action/upload-sarif@v4`, which surfaces findings
  inline in PRs.   What Trivy actually catches:

  - **OS-level CVEs** in the base image's packages (e.g., a glibc
    CVE in the build stage).
  - **Go module vulnerabilities** from the binary's pinned
    dependencies (via Go's vulnerability database).
  - **Misconfigurations** — Trivy also has a config-scanning mode for
    Dockerfile patterns (running as root, missing HEALTHCHECK, etc.),
    though we don't run that mode today.
  - **Secrets accidentally baked in** — Trivy looks for high-entropy
    strings and known credential patterns.
