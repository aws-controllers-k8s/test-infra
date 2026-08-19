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
#
# SEED ONLY. This exists so ACK can bootstrap, and ACK replaces it once it adopts the
# role: on a live cluster the role carries ACK's own six inline policies
# (EC2Management, ECRManagement, EKSSelfManagement, IAMManagement, Route53Management,
# S3ProwLogs) and BootstrapPermissions is GONE. EKSSelfManagement grants the same eks:*
# on the same five ARN patterns, and IAMManagement grants iam:* on
# role/<stack>-* which is a superset of the one role this granted. So it is fully
# superseded, and the capability is ACTIVE without it.
#
# WHY THE COUNT, rather than just ignore_changes. `ignore_changes` suppresses diffs on an
# EXISTING object; it does nothing when the object is absent. Because ACK deletes this
# policy, every subsequent plan wanted to CREATE it again -- permanent noise on an
# otherwise clean plan, and it would re-add a policy ACK deliberately replaced. Gating on
# a variable expresses the real lifecycle: Terraform seeds it on a fresh bootstrap and
# never touches it again.
#
# Fresh bootstrap:  terraform apply -var seed_ack_bootstrap_policy=true
# Then, once ACK has adopted the role and written its own policies, drop the flag (the
# default) and remove the seed from state WITHOUT destroying it:
#   terraform state rm 'aws_iam_role_policy.ack_capability_bootstrap[0]'
# Check `aws iam list-role-policies` first: if BootstrapPermissions is somehow still
# present, flipping the flag would DELETE it rather than orphan it.
resource "aws_iam_role_policy" "ack_capability_bootstrap" {
  count = var.seed_ack_bootstrap_policy ? 1 : 0

  name = "BootstrapPermissions"
  role = aws_iam_role.ack_capability.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = ["eks:*"]
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


