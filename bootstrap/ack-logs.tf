################################################################################
# ACK capability controller log delivery.
#
# Same compensating control as argocd-logs.tf, for the other capability: the ACK
# controllers run on AWS-managed infrastructure outside the cluster, so they have no
# pods in this cluster and `kubectl logs` cannot reach them. Without delivery there is
# no way to see a reconcile decision at all.
#
# That gap has already cost us. When the build-cluster Cluster CR wedged on
# 2026-09-28 (ACK.ResourceSynced=True, Ready=False, "The type for cluster update was
# not provided"), the only evidence available was the CR's condition message plus the
# CloudTrail record of the rejected UpdateClusterConfig. Which spec field the delta was
# on had to be inferred from controller source rather than read from a log line.
#
# Wired through the CloudWatch Logs API, not the EKS capability API: a delivery SOURCE
# (the capability ARN plus a log type), a delivery DESTINATION (the log group), and a
# DELIVERY joining them.
#
# NOTE ON THE MISSING time_sleep CHAIN. argocd-logs.tf serializes its sources behind
# time_sleep because PutDeliverySource modifies the capability, the capability accepts
# one modification at a time, and Argo CD has five log types contending for it. ACK
# publishes exactly ONE log type, so there is nothing to serialize against and no chain
# is needed here. Confirm before adding a second source:
#
#   aws logs describe-configuration-templates --service eks \
#     --query 'configurationTemplates[?resourceType==`ack`].logType'
#
# The one collision that remains is external: PutDeliverySource modifies the same
# capability the in-cluster Capability CR reconciles, so a first apply can race ACK and
# return ConflictException. Re-running apply settles it. The CR cannot fight the
# delivery config itself, because logging is not part of the Capability CRD spec.
################################################################################

variable "ack_log_retention_days" {
  description = "Retention for the ACK controller log group. Kept short; these are for troubleshooting, not audit."
  type        = number
  default     = 14
}

resource "aws_cloudwatch_log_group" "ack_controllers" {
  name              = "/aws/eks/${local.cluster_name}/capability/ack"
  retention_in_days = var.ack_log_retention_days
}

resource "aws_cloudwatch_log_delivery_destination" "ack" {
  name          = "${local.stack_name}-ack-controllers"
  output_format = "json"

  delivery_destination_configuration {
    destination_resource_arn = aws_cloudwatch_log_group.ack_controllers.arn
  }
}

resource "aws_cloudwatch_log_delivery_source" "ack" {
  name         = "${local.stack_name}-ack"
  log_type     = "EKS_CAPABILITY_ACK_LOGS"
  resource_arn = awscc_eks_capability.ack.arn
}

resource "aws_cloudwatch_log_delivery" "ack" {
  delivery_source_name     = aws_cloudwatch_log_delivery_source.ack.name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.ack.arn
}

output "ack_log_group" {
  description = "CloudWatch log group receiving ACK capability controller logs."
  value       = aws_cloudwatch_log_group.ack_controllers.name
}
