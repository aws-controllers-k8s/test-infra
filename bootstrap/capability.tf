################################################################################
# ACK Capability IAM Role
# Single role used by both Terraform (to create the capability) and ACK
# (to reconcile resources). ACK adopts this role via adopt-or-create and
# adds the full set of inline policies. Terraform only seeds the minimal
# permissions needed for ACK to bootstrap (EKS + IAM on itself).
################################################################################

resource "aws_iam_role" "ack_capability" {
  name = "${local.stack_name}-ack-capability-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "capabilities.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })

  # ACK manages inline policies after adoption — don't fight over them.
  lifecycle {
    ignore_changes = all
  }
}

# Minimal bootstrap permissions — just enough for ACK to:
# 1. Manage the capability itself (eks:*)
# 2. Update its own role policies (iam:* on itself)
resource "aws_iam_role_policy" "ack_capability_bootstrap" {
  name = "BootstrapPermissions"
  role = aws_iam_role.ack_capability.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["eks:*"]
        Resource = [
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:capability/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:access-entry/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:podidentityassociation/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:addon/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:cluster/${local.cluster_name}"
        ]
      },
      {
        Effect   = "Allow"
        Action   = ["iam:*"]
        Resource = "arn:${local.partition}:iam::${local.account_id}:role/${local.stack_name}-ack-capability-role"
      }
    ]
  })

  lifecycle {
    ignore_changes = all
  }
}

################################################################################
# ACK Capability
#
# Destroyed LAST to ensure ACK controllers (managed by Flux) have time to
# clean up their resources before the capability is removed.
################################################################################

resource "awscc_eks_capability" "ack" {
  cluster_name              = aws_eks_cluster.this.name
  capability_name           = "ack-eks"
  type                      = "ACK"
  role_arn                  = aws_iam_role.ack_capability.arn
  delete_propagation_policy = "RETAIN"

  lifecycle {
    ignore_changes = all
  }

  depends_on = [aws_eks_cluster.this]
}


