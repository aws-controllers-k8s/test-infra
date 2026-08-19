# ACK-Managed EKS Cluster

These ACK manifests define the cluster's desired state. The self-management loop:

```
Push to git → Flux syncs → ACK EKS controller reconciles → EKS API updates cluster
```

## Directory Structure

| Directory | What it manages |
|-----------|----------------|
| `capability/` | ACK EKS capability + IAM role |
| `cluster/` | Control-plane cluster config, access entries, nodepool, storage class |
| `cluster/addons/` | EKS managed addons (Secrets Store CSI, etc.) + addon roles |
| `cluster/pod-identities/` | IAM roles, namespaces, and pod identity associations |
| `build-cluster/` | Dedicated Prow build cluster (VPC, subnets, NAT, IAM roles, EKS cluster, access entries, pod identities) |
| `prow/` | S3 logs bucket, Route53 DNS, Supernova role |
| `flux/` | ECR pull-through cache for Flux images |

## Making changes

Edit the manifest, push. Flux reconciles within a few minutes.

Examples:
- **Upgrade Kubernetes**: change `spec.version` in `cluster/cluster.yaml`
- **Add an addon**: add a new `Addon` resource to `cluster/addons/addons.yaml`
- **Grant access**: add a new `AccessEntry` to `cluster/access-entries.yaml`
- **Add a pod identity**: add Role + PodIdentityAssociation to `cluster/pod-identities/`

## Deploying new infrastructure as ACK manifests

All AWS infrastructure changes go through ACK manifests in this directory.
Push a manifest, Flux syncs it, ACK reconciles the AWS resource.

### Adding an EKS addon with pod identity

1. **Create the IAM role** in `cluster/addons/roles/`:

```yaml
# cluster/addons/roles/my-addon-role.yaml
apiVersion: iam.services.k8s.aws/v1alpha1
kind: Role
metadata:
  name: my-addon-role
  namespace: ack-system
spec:
  name: ${STACK_NAME}-my-addon-role
  assumeRolePolicyDocument: |
    {
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Principal": { "Service": "pods.eks.amazonaws.com" },
          "Action": ["sts:AssumeRole", "sts:TagSession"]
        }
      ]
    }
  # Use 'policies' for AWS managed policies (list of ARN strings)
  policies:
  - arn:aws:iam::aws:policy/service-role/MyAddonPolicy
  # Or use 'inlinePolicies' for custom policies (map of name → JSON string)
  # inlinePolicies:
  #   MyCustomPolicy: |
  #     { "Version": "2012-10-17", "Statement": [...] }
```

2. **Register the role** in `cluster/addons/roles/kustomization.yaml`:

```yaml
resources:
- external-dns-role.yaml
- ebs-csi-role.yaml
- my-addon-role.yaml  # ← add here
```

3. **Add the addon** to `cluster/addons/addons.yaml`:

```yaml
---
apiVersion: eks.services.k8s.aws/v1alpha1
kind: Addon
metadata:
  name: my-addon
  namespace: ack-system
spec:
  clusterName: ${STACK_NAME}-cluster
  name: aws-my-addon          # EKS addon name
  podIdentityAssociations:
  - roleARN: arn:aws:iam::${ACCOUNT_ID}:role/${STACK_NAME}-my-addon-role
    serviceAccount: my-addon-sa  # service account the addon uses
```

4. **Push and reconcile.** The dependency chain ensures roles are created
   before addons: `ack-addons-roles` → `ack-addons`.

### Adding a standalone pod identity (non-addon workloads)

1. Create the IAM role in `cluster/pod-identities/roles/`
2. Add a `PodIdentityAssociation` to `cluster/pod-identities/`
3. Register both in their respective `kustomization.yaml` files

### Adding other AWS resources (S3, Route53, ECR, etc.)

1. Place the ACK manifest in the appropriate directory (or create a new one)
2. Add it to the directory's `kustomization.yaml`
3. Ensure the ACK capability role has permissions for the resource type —
   update `capability/role/ack-capability-role.yaml` if needed

### Key patterns

- **Trust policy for pod identity:** Always use `pods.eks.amazonaws.com` as the
  principal with `sts:AssumeRole` and `sts:TagSession` actions.
- **Managed policies:** Use `spec.policies` (list of ARN strings).
- **Inline policies:** Use `spec.inlinePolicies` (map of policy name → JSON string).
- **Deletion protection:** Add `services.k8s.aws/deletion-policy: retain` annotation
  to resources that should survive CR deletion (hosted zones, S3 buckets, etc.).
- **Adopt existing resources:** Use `services.k8s.aws/adoption-policy: adopt-or-create`
  with `services.k8s.aws/adoption-fields` to adopt Terraform-created resources.
- **Per-environment values:** declare them in the chart's `values.yaml` and have the Argo CD
  Application pass them as helm parameters from Terraform (`bootstrap/argocd-applications.tf`,
  `local.argocd_chart_values`). Do **not** use `${VAR}` placeholders — those were resolved by
  Flux's postBuild substitution, which Argo CD has no equivalent for.

### Forcing reconciliation

```bash
# Re-read git and re-evaluate one Application
kubectl annotate applications.argoproj.io <name> -n argocd \
  argocd.argoproj.io/refresh=hard --overwrite

# Note: refreshing the root (root-applications) does NOT re-render its children.
# Refresh the Application that owns the path you changed.
```

## Per-environment values

These charts take per-environment values as **helm parameters on their Argo CD Application**,
composed by Terraform in `local.argocd_chart_values` (`bootstrap/argocd-applications.tf`). Which
parameters each chart needs is declared per path in `argocd/applications/values.yaml`.

Values that are static and git-authored belong in the chart's own `values.yaml` instead — only
things Terraform alone knows (account id, region, stack name, domains, repo coordinates) travel
as parameters.

Historically these manifests used `${VAR}` placeholders substituted by Flux from a
`self-managed-vars` ConfigMap. Argo CD renders off-cluster and has no substitution step, so that
mechanism and the ConfigMap are gone; see `docs/argocd-migration.md`.
