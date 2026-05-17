# Cluster Bootstrap (Terraform)

Minimal Terraform to solve the chicken-and-egg problem. After bootstrap,
Flux manages its own upgrades and ACK manages the cluster.

## Prerequisites

- Terraform >= 1.5
- AWS CLI configured with admin permissions
- Docker installed (for building the bootstrap image)
- `kubectl` installed
- `helm` installed
- `yq` installed
- Access to `public.ecr.aws` (for pushing/pulling images)

## First-Time Bootstrap

### 1. Create the Terraform state backend

```bash
cd test-infra/bootstrap
./scripts/bootstrap-backend.sh
```

This finds or creates an S3 bucket with prefix `ack-test-infra-terraform-state`
(appending a random suffix for global uniqueness) and writes `backend.tf` with
the bucket name. The script is idempotent — on subsequent runs it reuses the
existing bucket.

### 2. Vendor the Flux chart

```bash
cd test-infra
./scripts/pull-flux-chart.sh
git add charts/flux2-*/
git commit -m "chore(flux): vendor flux2 chart"
git push
```

### 3. Create required AWS Secrets Manager secrets

Prow requires the following secrets to exist before deployment:

```bash
# GitHub App credentials (JSON with cert and appid fields)
aws secretsmanager create-secret \
  --name "ack/prow/github-token" \
  --secret-string '{"cert":"<PEM_PRIVATE_KEY>","appid":"<APP_ID>"}'

# GitHub webhook HMAC token
aws secretsmanager create-secret \
  --name "ack/prow/hmac-token" \
  --secret-string "<HMAC_SECRET>"

# GitHub Personal Access Token (for PR operations)
aws secretsmanager create-secret \
  --name "ack/prow/github-pat-token" \
  --secret-string "<PAT_TOKEN>"

# API Model Knowledge Base ID (used by the add-resource workflow agent)
aws secretsmanager create-secret \
  --name "ack/prow/api-model-kb" \
  --secret-string "<KNOWLEDGE_BASE_ID>"

# ECR pull-through cache credentials for ghcr.io/fluxcd
aws secretsmanager create-secret \
  --name "ecr-pullthroughcache/ghcr-fluxcd" \
  --secret-string '{"username":"<GITHUB_USER>","accessToken":"<GITHUB_PAT>"}'
```

### 4. Generate your environment

```bash
cd test-infra/bootstrap
./gen-env
```

This prompts for each variable (region, flux version, GitHub org/repo/branch)
and writes a `.tfvars` file to `bootstrap/environment/dev.tfvars`.

### 5. Bootstrap the cluster

```bash
cd test-infra/bootstrap
terraform init
terraform apply -var-file=environment/dev.tfvars
```

This will:
- Create a VPC and EKS Auto Mode cluster (with the built-in `general-purpose` NodePool enabled to bootstrap Flux)
- Install Flux from the vendored chart
- Create the ACK EKS capability
- Build and push the `prow-build-prow-images` builder image to public ECR
- Create a public ECR repository for all Prow images
- Deploy ConfigMaps that bridge Terraform outputs to Flux

Once Flux is running, it reconciles the ACK `Cluster` resource, which disables
the built-in `general-purpose` NodePool and the custom `prow-compute` NodePool
(c6a.8xlarge) takes over. This handoff happens automatically within a few
minutes of the cluster becoming ready.

### 6. Verify deployment

```bash
# Configure kubectl
$(terraform output -raw kubeconfig_command)

# Verify Flux is syncing
kubectl get kustomizations -A

# Verify ACK resources are healthy
kubectl get role.iam,capability.eks,cluster.eks,accessentry.eks -n ack-system

# Verify Prow is running (after prow.yaml is enabled)
kubectl get pods -n prow
```

### 7. Configure GitHub webhook

After Prow is deployed, retrieve the webhook endpoint:

```bash
# Get the hook service hostname
HOOK_HOST=$(kubectl get svc hook -n prow -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
echo "Webhook URL: http://${HOOK_HOST}:8888/hook"
```

Configure your GitHub App/org webhook to:
- **URL:** `http://<hostname>:8888/hook`
- **Content type:** `application/json`
- **Secret:** same value used in `ack/prow/hmac-token` secret

> **Note:** The webhook endpoint changes each time the cluster is recreated.
> After a fresh bootstrap, update the GitHub webhook URL with the new hostname.

## Accessing Grafana

```bash
# Get the Grafana endpoint
kubectl get svc -n prometheus prometheus-prometheus-grafana \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

## Accessing Deck UI

```bash
# Get the Deck endpoint
kubectl get svc -n prow deck \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

## Architecture

### What Terraform owns (bootstrap only)

| Resource | Purpose |
|----------|---------|
| VPC + subnets | Network foundation |
| EKS cluster | Compute platform |
| ACK capability + bootstrap IAM role | Enables ACK self-management |
| Flux (vendored Helm chart) | GitOps engine |
| GitRepository + Kustomization | Flux sync configuration |
| `self-managed-vars` ConfigMap | Bridges Terraform outputs → Flux |
| Public ECR repository | Prow image storage |
| Webhook security group | GitHub → Prow connectivity |
| Builder image push | Seeds the image build pipeline |

### What Flux/ACK own (day-2)

| What | Where |
|------|-------|
| Flux upgrades | `flux/flux/helm-release.yaml` |
| Cluster config (logging, version) | `flux/ack/cluster/cluster.yaml` |
| Access entries | `flux/ack/cluster/access-entries.yaml` |
| EKS addons | `flux/ack/cluster/addons/addons.yaml` |
| ACK capability role | `flux/ack/capability/role/ack-capability-role.yaml` |
| Prow IAM roles + Pod Identities | `flux/ack/cluster/pod-identities/` |
| Prow image builds | `flux/prow/build-images/build-job.yaml` |
| Prow deployment | `flux/prow/charts/` |
| S3 bucket, DNS, Supernova role | `flux/ack/prow/` |
| ECR pull-through cache | `flux/ack/flux/ecr-pull-through-cache.yaml` |

### Dependency chain

```
Terraform bootstrap
    ↓
ack-capability-role ─→ ack-capability ─→ ack-cluster ─┬─→ ack-addons (+ ack-addons-roles)
ack-addons-roles (no deps)                             ├─→ ack-pod-identities
                                                       ├─→ ack-prow
                                                       └─→ ack-flux
                                                              ↓
                                              prow-crds ─→ prow-build-images ─→ prow-charts ─→ prometheus
```

## Upgrading Flux

```bash
# 1. Update the version
yq -i '.version = "2.9.0"' flux/flux-version.yaml

# 2. Vendor the new chart
./scripts/pull-flux-chart.sh

# 3. Commit and push
git add charts/ flux/
git commit -m "chore(flux): upgrade to 2.9.0"
git push
# Flux self-upgrades on next reconciliation
```

## Re-running Terraform

Safe to re-run anytime. Only needed when:
- Rebuilding the cluster from scratch
- Adding new values to the `self-managed-vars` ConfigMap
- Changing VPC/networking or security groups

```bash
terraform apply -var-file=environment/dev.tfvars
```

## Tearing Down

The stack includes a `null_resource.flux_suspend` that automatically strips
Flux finalizers before Helm uninstalls the controllers. This should allow a
clean one-shot destroy:

```bash
cd test-infra/bootstrap
terraform destroy -var-file=environment/dev.tfvars
```

### If destroy gets stuck

If the destroy hangs (e.g., from a previous partial destroy where controllers
are already gone), remove the in-cluster resources from state and retry:

```bash
cd test-infra/bootstrap

# Remove in-cluster resources that block destroy
terraform state rm helm_release.flux
terraform state rm kubernetes_namespace_v1.flux_system
terraform state rm kubernetes_namespace_v1.ack_system
terraform state rm kubectl_manifest.flux_git_source
terraform state rm kubectl_manifest.flux_kustomization
terraform state rm kubernetes_config_map_v1.self_managed_vars
terraform state rm kubernetes_config_map_v1.flux_version

# Destroy everything else
terraform destroy
```

## Cleaning Up the State Backend

After destroying all infrastructure, you can remove the Terraform state backend.
This is irreversible — only do this if you're fully decommissioning the environment.

```bash
cd test-infra/bootstrap
./scripts/cleanup-backend.sh
```
