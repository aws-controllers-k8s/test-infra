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
# Teardown is explicit and only for account decommissioning - see the Teardown note in
# docs/argocd-migration.md. No prevent_destroy is set, because that would make
# deliberate teardown impossible.

provider "aws" {
  region = var.region
}

provider "awscc" {
  region = var.region
}

locals {
  name_prefix = "ack-test-infra-${var.stage}"
}

# Account instance of IAM Identity Center. Supported for standalone accounts that are
# not managed by AWS Organizations, which is the case here. One per account, usable
# only within this account and region.
resource "awscc_sso_instance" "this" {
  name = local.name_prefix
}

# Group mapped to the Argo CD ADMIN role in the capability's rbacRoleMappings.
# The capability references this by group_id, with identity type SSO_GROUP.
resource "aws_identitystore_group" "argocd_admins" {
  identity_store_id = awscc_sso_instance.this.identity_store_id
  display_name      = "${local.name_prefix}-argocd-admins"
  description       = "Argo CD ADMIN role for the ACK test-infra EKS capability"
}

# Optional users. Empty by default - see the argocd_admins variable description.
resource "aws_identitystore_user" "admins" {
  for_each = { for u in var.argocd_admins : u.user_name => u }

  identity_store_id = awscc_sso_instance.this.identity_store_id
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

  identity_store_id = awscc_sso_instance.this.identity_store_id
  group_id          = aws_identitystore_group.argocd_admins.group_id
  member_id         = each.value.user_id
}
