output "cluster_name" {
  description = "EKS cluster name"
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "EKS cluster API endpoint"
  value       = module.eks.cluster_endpoint
}

output "cluster_arn" {
  description = "EKS cluster ARN"
  value       = module.eks.cluster_arn
}

output "ack_capability_role_arn" {
  description = "IAM role ARN for the ACK EKS capability"
  value       = aws_iam_role.ack_capability.arn
}

output "cluster_admin_role_arn" {
  description = "IAM role ARN for cluster admin break-glass access"
  value       = aws_iam_role.cluster_admin.arn
}

output "vpc_id" {
  description = "VPC ID"
  value       = local.vpc_id
}

output "flux_version" {
  description = "Flux distribution version installed"
  value       = var.flux_version
}

output "kubeconfig_command" {
  description = "Command to configure kubectl"
  value       = "aws eks update-kubeconfig --name ${module.eks.cluster_name} --region ${var.region}"
}
