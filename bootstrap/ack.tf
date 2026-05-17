################################################################################
# ACK Namespace & Resource Cleanup
################################################################################

resource "kubernetes_namespace_v1" "ack_system" {
  metadata {
    name = "ack-system"
  }

  depends_on = [aws_eks_cluster.this, awscc_eks_capability.ack, aws_iam_role_policy.ack_capability_initial]
}


resource "null_resource" "cleanup_ack_resources" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
    script       = "${path.module}/scripts/cleanup-ack-resources.sh"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "${self.triggers.script} ${self.triggers.cluster_name} ${self.triggers.region}"
  }

  depends_on = [null_resource.cleanup_ack_capability_role, null_resource.cleanup_prow_logs_bucket, null_resource.cleanup_prow_hosted_zone]
}

# Cleans up the ACK capability role (created by ACK in-cluster, retained
# during destroy via deletion-policy: retain on the Role CR).
resource "null_resource" "cleanup_ack_capability_role" {
  triggers = {
    role_name = "${local.stack_name}-ack-capability-role"
    region    = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      echo "Cleaning up ACK capability role: ${self.triggers.role_name}"

      # Delete all inline policies first
      for policy in $(aws iam list-role-policies --role-name ${self.triggers.role_name} --query 'PolicyNames[]' --output text --region ${self.triggers.region} 2>/dev/null); do
        aws iam delete-role-policy --role-name ${self.triggers.role_name} --policy-name "$policy" --region ${self.triggers.region} 2>/dev/null || true
      done

      # Detach all managed policies
      for policy_arn in $(aws iam list-attached-role-policies --role-name ${self.triggers.role_name} --query 'AttachedPolicies[].PolicyArn' --output text --region ${self.triggers.region} 2>/dev/null); do
        aws iam detach-role-policy --role-name ${self.triggers.role_name} --policy-arn "$policy_arn" --region ${self.triggers.region} 2>/dev/null || true
      done

      # Delete the role
      aws iam delete-role --role-name ${self.triggers.role_name} --region ${self.triggers.region} 2>/dev/null || true

      echo "ACK capability role cleanup complete"
    EOT
    on_failure = continue
  }

    depends_on = [kubernetes_namespace_v1.ack_system]

}

# Empties and deletes the Prow logs S3 bucket (retained by ACK).
resource "null_resource" "cleanup_prow_logs_bucket" {
  triggers = {
    bucket_name = "${local.stack_name}-prow-logs"
    region      = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      echo "Cleaning up Prow logs bucket: ${self.triggers.bucket_name}"

      # Empty the bucket (delete all objects and versions)
      aws s3 rm "s3://${self.triggers.bucket_name}" --recursive --region ${self.triggers.region} 2>/dev/null || true

      # Delete any remaining object versions and delete markers
      aws s3api list-object-versions --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} --query '{Objects: [].{Key:Key,VersionId:VersionId}}' --output json 2>/dev/null | \
        aws s3api delete-objects --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} --delete file:///dev/stdin 2>/dev/null || true

      # Delete the bucket
      aws s3api delete-bucket --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} 2>/dev/null || true

      echo "Prow logs bucket cleanup complete"
    EOT
    on_failure = continue
  }

  depends_on = [kubernetes_namespace_v1.ack_system]
}

# Deletes all non-required record sets and then the Route53 hosted zone
# (retained by ACK via deletion-policy: retain on the HostedZone CR).
resource "null_resource" "cleanup_prow_hosted_zone" {
  triggers = {
    prow_domain = var.prow_domain
    region      = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      echo "Cleaning up Prow hosted zone: ${self.triggers.prow_domain}"

      # Find the hosted zone ID
      ZONE_ID=$(aws route53 list-hosted-zones-by-name --dns-name "${self.triggers.prow_domain}" --query "HostedZones[?Name=='${self.triggers.prow_domain}.'].Id" --output text 2>/dev/null | head -1 | sed 's|/hostedzone/||')

      if [ -z "$ZONE_ID" ]; then
        echo "  Hosted zone not found. Nothing to clean up."
        exit 0
      fi

      echo "  Found hosted zone: $ZONE_ID"

      # Delete all non-required record sets (skip NS and SOA for the zone apex)
      aws route53 list-resource-record-sets --hosted-zone-id "$ZONE_ID" --query "ResourceRecordSets[?Type != 'NS' && Type != 'SOA']" --output json 2>/dev/null | \
        python3 -c "
import json, sys
records = json.load(sys.stdin)
if not records:
    sys.exit(0)
changes = [{'Action': 'DELETE', 'ResourceRecordSet': r} for r in records]
batch = {'Changes': changes}
print(json.dumps(batch))
" | while read -r batch; do
        if [ -n "$batch" ] && [ "$batch" != "null" ]; then
          aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "$batch" --region ${self.triggers.region} 2>/dev/null || true
        fi
      done

      # Delete the hosted zone
      aws route53 delete-hosted-zone --id "$ZONE_ID" --region ${self.triggers.region} 2>/dev/null || true

      echo "Prow hosted zone cleanup complete"
    EOT
    on_failure = continue
  }

  depends_on = [kubernetes_namespace_v1.ack_system]
}
