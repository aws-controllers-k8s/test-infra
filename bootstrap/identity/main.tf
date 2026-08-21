# IAM Identity Center account instance for the Argo CD EKS Capability.
#
# This module is deliberately SEPARATE from the main bootstrap stack and holds its
# own Terraform state. The Argo CD capability requires an IdC instance ARN
# (argoCd.awsIdc.idcInstanceArn is a required API field), and an account instance is
# a singleton whose ARN is baked into the capability configuration. Keeping it out of
# the main stack means the routine destroy/apply cycle can never delete it and force
# a capability recreation.
#
# Apply once per account. The main stack consumes the results through the
# aws_ssoadmin_instances and aws_identitystore_group data sources, not through a
# cross-state reference.
#
# Teardown is explicit and only for account decommissioning. No prevent_destroy is set,
# because that would make deliberate teardown impossible.

provider "aws" {
  region = var.region
}

provider "awscc" {
  region = var.region
}

locals {
  name_prefix = "ack-test-infra-${var.stage}"
}

# IAM Identity Center instance.
#
# WHICH KIND YOU GET DEPENDS ON THE ACCOUNT, and the two are not interchangeable:
#
#   ACCOUNT instance  (create_account_instance = true, the default)
#     Created here via AWS::SSO::Instance. Only valid for a standalone account, or a
#     MEMBER account of an organization. One per account, usable only in this account
#     and region.
#
#   ORGANIZATION instance (create_account_instance = false)
#     Must already exist, and can only be enabled from the organization's MANAGEMENT
#     account through the IAM Identity Center console -- there is no API for it. This
#     module then consumes it rather than creating anything.
#
# CreateInstance is REJECTED in an organization management account. Prod is the
# management account of its organization, and the failure is explicit rather than
# subtle:
#
#   Organization management account is not allowed to perform the operation.
#   (Service: SsoAdmin, Status Code: 400)
#
# So prod runs with create_account_instance = false and an organization instance
# enabled by hand first. Staging is standalone and creates its own account instance.
resource "awscc_sso_instance" "this" {
  count = var.create_account_instance ? 1 : 0

  name = local.name_prefix
}

# The existing organization instance, when this module is not creating one. There is at
# most one instance visible to an account, so the lookup is deterministic.
data "aws_ssoadmin_instances" "existing" {
  count = var.create_account_instance ? 0 : 1
}

locals {
  identity_store_id = var.create_account_instance ? (
    awscc_sso_instance.this[0].identity_store_id
    ) : (
    one(data.aws_ssoadmin_instances.existing[0].identity_store_ids)
  )

  instance_arn = var.create_account_instance ? (
    awscc_sso_instance.this[0].instance_arn
    ) : (
    one(data.aws_ssoadmin_instances.existing[0].arns)
  )
}

# Group mapped to the Argo CD ADMIN role in the capability's rbacRoleMappings.
# The capability references this by group_id, with identity type SSO_GROUP.
resource "aws_identitystore_group" "argocd_admins" {
  identity_store_id = local.identity_store_id
  display_name      = "${local.name_prefix}-argocd-admins"
  description       = "Argo CD ADMIN role for the ACK test-infra EKS capability"
}

# Optional users. Empty by default - see the argocd_admins variable description.
resource "aws_identitystore_user" "admins" {
  for_each = { for u in var.argocd_admins : u.user_name => u }

  identity_store_id = local.identity_store_id
  user_name         = each.value.user_name
  display_name      = "${each.value.given_name} ${each.value.family_name}"

  name {
    given_name  = each.value.given_name
    family_name = each.value.family_name
  }

  emails {
    value   = each.value.email
    primary = true
  }
}

resource "aws_identitystore_group_membership" "admins" {
  for_each = aws_identitystore_user.admins

  identity_store_id = local.identity_store_id
  group_id          = aws_identitystore_group.argocd_admins.group_id
  member_id         = each.value.user_id
}
