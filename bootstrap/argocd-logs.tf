################################################################################
# Argo CD capability controller log delivery (plan section 3.7)
#
# The capability's controllers run on AWS-managed infrastructure outside the
# cluster, so Prometheus cannot scrape them and `kubectl logs` cannot reach them.
# Log delivery is the compensating control for that observability gap (D7).
#
# Delivery is CloudWatch Vended Logs, configured through the CloudWatch Logs API
# rather than the EKS capability API: a delivery SOURCE per log type (the capability
# ARN plus a log type), a delivery DESTINATION (the log group), and a DELIVERY
# joining them.
#
# *** The sources MUST be created serially, WITH a settling delay between them. ***
#
# PutDeliverySource modifies the capability, and the capability accepts only one
# modification at a time:
#
#   ConflictException: Capability argocd cannot be updated as it is currently
#   being modified by another request
#
# A for_each over log types creates them in parallel and most of them fail. Ordering
# alone is not sufficient either: the capability stays in a modifying state for a
# short period AFTER PutDeliverySource returns, so a bare depends_on chain still
# collides. Hence explicit resources chained through time_sleep.
#
# This keeps a plain `terraform apply` correct without needing -parallelism=1, which
# a fresh bootstrap would not know to pass.
#
# To add a log type, add a resource and extend the chain. The five available are:
#   EKS_CAPABILITY_ARGOCD_APPLICATION_LOGS      sync and health decisions
#   EKS_CAPABILITY_ARGOCD_APPLICATIONSET_LOGS   generator expansion
#   EKS_CAPABILITY_ARGOCD_REPOSERVER_LOGS       manifest generation
#   EKS_CAPABILITY_ARGOCD_SERVER_LOGS           API and UI access
#   EKS_CAPABILITY_ARGOCD_COMMITSERVER_LOGS     git write-back (unused here)
#
# The three enabled below are the ones that matter during the migration. SERVER is
# API/UI access logging and COMMITSERVER covers git write-back, which is not used.
################################################################################

variable "argocd_log_retention_days" {
  description = "Retention for the Argo CD controller log group. Kept short; these are for troubleshooting, not audit."
  type        = number
  default     = 14
}

resource "aws_cloudwatch_log_group" "argocd_controllers" {
  name              = "/aws/eks/${local.cluster_name}/capability/argocd"
  retention_in_days = var.argocd_log_retention_days
}

resource "aws_cloudwatch_log_delivery_destination" "argocd" {
  name          = "${local.stack_name}-argocd-controllers"
  output_format = "json"

  delivery_destination_configuration {
    destination_resource_arn = aws_cloudwatch_log_group.argocd_controllers.arn
  }
}

# --- Delivery sources: serialized chain, see the header note ---

resource "aws_cloudwatch_log_delivery_source" "application" {
  name         = "${local.stack_name}-argocd-application"
  log_type     = "EKS_CAPABILITY_ARGOCD_APPLICATION_LOGS"
  resource_arn = awscc_eks_capability.argocd.arn
}

resource "time_sleep" "argocd_log_source_1" {
  depends_on      = [aws_cloudwatch_log_delivery_source.application]
  create_duration = "45s"
}

resource "aws_cloudwatch_log_delivery_source" "applicationset" {
  name         = "${local.stack_name}-argocd-applicationset"
  log_type     = "EKS_CAPABILITY_ARGOCD_APPLICATIONSET_LOGS"
  resource_arn = awscc_eks_capability.argocd.arn

  depends_on = [time_sleep.argocd_log_source_1]
}

resource "time_sleep" "argocd_log_source_2" {
  depends_on      = [aws_cloudwatch_log_delivery_source.applicationset]
  create_duration = "45s"
}

resource "aws_cloudwatch_log_delivery_source" "reposerver" {
  name         = "${local.stack_name}-argocd-reposerver"
  log_type     = "EKS_CAPABILITY_ARGOCD_REPOSERVER_LOGS"
  resource_arn = awscc_eks_capability.argocd.arn

  depends_on = [time_sleep.argocd_log_source_2]
}

# --- Deliveries: also chained, since each references the capability-backed source ---

resource "aws_cloudwatch_log_delivery" "application" {
  delivery_source_name     = aws_cloudwatch_log_delivery_source.application.name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.argocd.arn
}

resource "aws_cloudwatch_log_delivery" "applicationset" {
  delivery_source_name     = aws_cloudwatch_log_delivery_source.applicationset.name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.argocd.arn

  depends_on = [aws_cloudwatch_log_delivery.application]
}

resource "aws_cloudwatch_log_delivery" "reposerver" {
  delivery_source_name     = aws_cloudwatch_log_delivery_source.reposerver.name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.argocd.arn

  depends_on = [aws_cloudwatch_log_delivery.applicationset]
}

output "argocd_log_group" {
  description = "CloudWatch log group receiving Argo CD capability controller logs."
  value       = aws_cloudwatch_log_group.argocd_controllers.name
}
