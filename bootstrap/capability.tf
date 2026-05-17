################################################################################
# ACK Capability IAM Role
# Created by Terraform with initial permissions. ACK creates a new role
# (ack-capability-role) in-cluster and updates the capability to use it.
################################################################################

resource "aws_iam_role" "ack_capability" {
  name = "${local.stack_name}-ack-managed-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "capabilities.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

# Minimal initial permissions - just enough for ACK to:
# 1. Create the new capability role (iam:*)
# 2. Adopt and update the capability to use the new role (eks:*)
resource "aws_iam_role_policy" "ack_capability_initial" {
  name = "InitialPermissions"
  role = aws_iam_role.ack_capability.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["iam:*"]
        Resource = "arn:${local.partition}:iam::${local.account_id}:role/${local.stack_name}-ack-capability-role"
      },
      {
        Effect   = "Allow"
        Action   = ["iam:*"]
        Resource = "arn:${local.partition}:iam::${local.account_id}:role/${local.stack_name}-ack-managed-role"
      },
      {
        Effect   = "Allow"
        Action   = ["eks:*"]
        Resource = [
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:capability/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:access-entry/${local.cluster_name}/*",
          "arn:${local.partition}:eks:${var.region}:${local.account_id}:cluster/${local.cluster_name}"
        ]
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


