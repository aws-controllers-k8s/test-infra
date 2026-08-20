################################################################################
# Argo CD Capability — Identity Center lookup
#
# The IdC account instance and the Argo CD admin group are created by the
# separate bootstrap/identity/ module, which holds its own Terraform state so the
# routine destroy/apply cycle of this stack can never delete them. They are
# consumed here by data source rather than cross-state reference, which keeps the
# two modules decoupled.
#
# Apply bootstrap/identity/ first. There is at most one IdC account instance per
# account, so the lookup is deterministic.
################################################################################

data "aws_ssoadmin_instances" "this" {}

data "aws_identitystore_group" "argocd_admins" {
  identity_store_id = one(data.aws_ssoadmin_instances.this.identity_store_ids)

  alternate_identifier {
    unique_attribute {
      attribute_path  = "DisplayName"
      attribute_value = "${local.stack_name}-argocd-admins"
    }
  }
}

################################################################################
# Argo CD Capability IAM Role
#
# Unlike the ACK capability role, nothing else adopts or widens this role, so
# Terraform owns it outright and no ignore_changes is needed.
#
# Note what is NOT here: no Kubernetes RBAC. EKS automatically creates access
# entries carrying AmazonEKSArgoCDPolicy and AmazonEKSArgoCDClusterPolicy when the
# capability is created, which cover Argo CD's own operation. Workload deploy
# permissions are granted separately — see argocd-access.tf.
#
# Git access needs no IAM: the source repository is public HTTPS, so neither
# CodeConnections nor CodeCommit permissions are required.
################################################################################

resource "aws_iam_role" "argocd_capability" {
  name = "${local.stack_name}-argocd-capability-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "capabilities.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

resource "aws_iam_role_policy" "argocd_capability" {
  name = "ArgoCDCapabilityPermissions"
  role = aws_iam_role.argocd_capability.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # Secrets referenced directly from Application resources.
      {
        Effect = "Allow"
        Action = [
          "secretsmanager:GetSecretValue",
          "secretsmanager:DescribeSecret"
        ]
        Resource = "arn:${local.partition}:secretsmanager:${var.region}:${local.account_id}:secret:ack/prow/*"
      },
      # Helm charts sourced from ECR. GetAuthorizationToken is not resource-scopable.
      {
        Effect   = "Allow"
        Action   = ["ecr:GetAuthorizationToken"]
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability",
          "ecr:GetDownloadUrlForLayer",
          "ecr:BatchGetImage"
        ]
        Resource = "arn:${local.partition}:ecr:${var.region}:${local.account_id}:repository/*"
      }
    ]
  })
}

################################################################################
# Argo CD Capability
#
# Terraform must own this because Argo CD cannot bootstrap itself.
#
# ignore_changes = all because AWS upgrades the capability automatically and
# reports the running version back on the resource; Terraform must not fight it.
#
# The sleep guards an IAM propagation race. CreateCapability validates the role's
# trust policy server-side, and when the role was just created that validation can
# fail with "The trust policy for the provided role is invalid" even though the
# policy is correct. On a fresh bootstrap the ~10 minute cluster creation hides the
# race; when adding the capability to an existing cluster there is nothing to hide
# behind, and it fails reliably without this.
################################################################################

resource "time_sleep" "argocd_role_propagation" {
  depends_on = [
    aws_iam_role.argocd_capability,
    aws_iam_role_policy.argocd_capability,
  ]

  create_duration = "30s"
}

resource "awscc_eks_capability" "argocd" {
  cluster_name              = aws_eks_cluster.this.name
  capability_name           = "argocd"
  type                      = "ARGOCD"
  role_arn                  = aws_iam_role.argocd_capability.arn
  delete_propagation_policy = "RETAIN"

  configuration = {
    argo_cd = {
      namespace = "argocd"

      aws_idc = {
        idc_instance_arn = one(data.aws_ssoadmin_instances.this.arns)
      }

      # SsoIdentityType enum is SSO_USER | SSO_GROUP. Plain "GROUP" is invalid and
      # fails at runtime rather than at plan time.
      rbac_role_mappings = [{
        role = "ADMIN"
        identities = [{
          id   = data.aws_identitystore_group.argocd_admins.group_id
          type = "SSO_GROUP"
        }]
      }]
    }
  }

  lifecycle {
    ignore_changes = all
  }

  depends_on = [
    aws_eks_cluster.this,
    time_sleep.argocd_role_propagation,
  ]
}

output "argocd_server_url" {
  description = "Argo CD UI URL, reported back by the capability once ACTIVE."
  value       = try(awscc_eks_capability.argocd.configuration.argo_cd.server_url, null)
}
