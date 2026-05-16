# ACK-Managed EKS Cluster

These ACK manifests define the cluster's desired state. The self-management loop:

```
Push to git → Flux syncs → ACK EKS controller reconciles → EKS API updates cluster
```

## Directory Structure

| Directory | What it manages |
|-----------|----------------|
| `capability/` | ACK EKS capability + IAM role |
| `cluster/` | Cluster config, access entries, nodepool, storage class |
| `cluster/addons/` | EKS managed addons (Secrets Store CSI, etc.) + addon roles |
| `cluster/pod-identities/` | IAM roles, namespaces, and pod identity associations |
| `prow/` | S3 logs bucket, Route53 DNS, Supernova role |
| `flux/` | ECR pull-through cache for Flux images |

## Making changes

Edit the manifest, push. Flux reconciles within a few minutes.

Examples:
- **Upgrade Kubernetes**: change `spec.version` in `cluster/cluster.yaml`
- **Add an addon**: add a new `Addon` resource to `cluster/addons/addons.yaml`
- **Grant access**: add a new `AccessEntry` to `cluster/access-entries.yaml`
- **Add a pod identity**: add Role + PodIdentityAssociation to `cluster/pod-identities/`

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
| `ACCOUNT_ID` | AWS account ID |
| `REGION` | AWS region |
