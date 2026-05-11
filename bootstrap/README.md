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

### 1. Vendor the Flux chart

```bash
cd test-infra
./scripts/pull-flux-chart.sh
git add charts/flux2-*/
git commit -m "chore(flux): vendor flux2 chart"
git push
```

### 2. Create required AWS Secrets Manager secrets

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

# ECR pull-through cache credentials for ghcr.io/fluxcd
aws secretsmanager create-secret \
  --name "ecr-pullthroughcache/ghcr-fluxcd" \
  --secret-string '{"username":"<GITHUB_USER>","accessToken":"<GITHUB_PAT>"}'
```

### 3. Bootstrap the cluster

```bash
cd test-infra/bootstrap
terraform init
terraform apply
```

This will:
- Create a VPC and EKS Auto Mode cluster
- Install Flux from the vendored chart
- Create the ACK EKS capability
- Build and push the `prow-build-prow-images` builder image to public ECR
- Create a public ECR repository for all Prow images
- Deploy ConfigMaps that bridge Terraform outputs to Flux

### 4. Verify deployment

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

### 5. Configure GitHub webhook

After Prow is deployed, get the webhook URL:

```bash
kubectl get svc hook -n prow -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

Configure your GitHub App/org webhook to:
- URL: `http://<hostname>:8888/hook`
- Content type: `application/json`
- Secret: same as `ack/prow/hmac-token`

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
| Flux upgrades | `flux/flux-self/helm-release.yaml` |
| Cluster config (logging, version) | `flux/self-managed/cluster.yaml` |
| Access entries | `flux/self-managed/access-entries.yaml` |
| EKS addons | `flux/self-managed/addons.yaml` |
| ACK capability role (full perms) | `flux/self-managed/prereqs/ack-capability-role.yaml` |
| Prow IAM roles + Pod Identities | `flux/self-managed/prow-iam-roles.yaml` |
| Prow image builds | `flux/prow-build-images/build-job.yaml` |
| Prow deployment | `flux/prow-charts/` |
| S3 bucket, ECR pull-through cache | `flux/self-managed/` |

### Dependency chain

```
Terraform bootstrap
    ↓
flux-self → self-managed-prereqs → self-managed-capability → self-managed-cluster → self-managed-pod-identities
                                                                    ↓
                                                              prow-crds → prow-build-images → prow-charts
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

## Tearing Down

The stack includes a `null_resource.flux_suspend` that automatically strips
Flux finalizers before Helm uninstalls the controllers. This should allow a
clean one-shot destroy:

```bash
cd test-infra/bootstrap
terraform destroy
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
