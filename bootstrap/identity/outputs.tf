output "idc_instance_arn" {
  description = "IdC instance ARN. Consumed as argoCd.awsIdc.idcInstanceArn by the capability."
  value       = awscc_sso_instance.this.instance_arn
}

output "idc_identity_store_id" {
  description = "Identity Store ID backing the instance."
  value       = awscc_sso_instance.this.identity_store_id
}

output "argocd_admin_group_id" {
  description = "Group ID for the Argo CD ADMIN rbacRoleMapping (identity type SSO_GROUP)."
  value       = aws_identitystore_group.argocd_admins.group_id
}

output "argocd_admin_group_display_name" {
  description = "Display name the main stack looks up via the aws_identitystore_group data source."
  value       = aws_identitystore_group.argocd_admins.display_name
}
