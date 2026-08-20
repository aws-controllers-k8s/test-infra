variable "region" {
  description = "AWS region. The IdC account instance is region-locked and must match the clusters."
  type        = string
  default     = "us-west-2"
}

variable "stage" {
  description = "Deployment stage (dev, staging, prod). Used to name the instance and admin group."
  type        = string
}

variable "argocd_admins" {
  description = <<-EOT
    Users to create in the Identity Store and add to the Argo CD ADMIN group.

    Optional. The capability builds successfully against an empty group, so an empty
    list does not block bootstrap - but nobody can sign in to the Argo CD UI until
    the group has members. Populate this, or federate an external IdP into the
    instance instead.
  EOT

  type = list(object({
    user_name   = string
    given_name  = string
    family_name = string
    email       = string
  }))

  default = []
}

variable "create_account_instance" {
  description = <<-EOT
    Create an IAM Identity Center ACCOUNT instance in this account.

    True for a standalone account, or a member account of an organization. FALSE for an
    organization's MANAGEMENT account, where CreateInstance is rejected outright:

      Organization management account is not allowed to perform the operation.
      (Service: SsoAdmin, Status Code: 400)

    With false, an ORGANIZATION instance must already exist -- enabled by hand from the
    management account's IAM Identity Center console, because no API creates one -- and
    this module consumes it instead of creating anything.
  EOT
  type        = bool
  default     = true
}
