# Self-Managed EKS Cluster (ACK Manifests)

These ACK manifests define the cluster's desired state. The self-management loop:

```
Push to git → Flux syncs → ACK EKS controller reconciles → EKS API updates cluster
```

## Manifests

| File | What it manages |
|------|----------------|
| `cluster.yaml` | Cluster config: version, logging, compute, endpoints |
| `capability.yaml` | The ACK EKS capability itself |
| `access-entries.yaml` | IAM role → cluster access mappings |
| `addons.yaml` | EKS managed addons (Secrets Store CSI, etc.) |

## Making changes

Edit the manifest, push. Flux reconciles within 10 minutes.

Examples:
- **Upgrade Kubernetes**: change `spec.version` in `cluster.yaml`
- **Add an addon**: add a new `Addon` resource to `addons.yaml`
- **Grant access**: add a new `AccessEntry` to `access-entries.yaml`

## Variable substitution

These manifests use `${VAR}` syntax, substituted by Flux from the
`self-managed-vars` ConfigMap (created by Terraform bootstrap):

| Variable | Value |
|----------|-------|
| `CLUSTER_NAME` | EKS cluster name |
| `ACK_CAPABILITY_ROLE_ARN` | IAM role for the ACK capability |
| `CLUSTER_SG_ID` | Cluster security group ID |
| `CLUSTER_ADMIN_ROLE_ARN` | Break-glass admin role ARN |
| `ADMIN_ROLE_ARN` | Admin role ARN |
| `READONLY_ROLE_ARN` | ReadOnly role ARN |
