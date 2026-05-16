################################################################################
# VPC
################################################################################

locals {
  vpc_cidr           = "10.0.0.0/16"
  availability_zones = ["us-west-2a", "us-west-2b"]
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"

  name = "${local.cluster_name}-vpc"
  cidr = local.vpc_cidr

  azs             = local.availability_zones
  private_subnets = [for i, az in local.availability_zones : cidrsubnet(local.vpc_cidr, 4, i)]
  public_subnets  = [for i, az in local.availability_zones : cidrsubnet(local.vpc_cidr, 4, i + 4)]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true
  enable_dns_support   = true

  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
}

################################################################################
# EKS Cluster IAM Role
################################################################################

resource "aws_iam_role" "cluster" {
  name = "${local.cluster_name}-cluster-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

resource "aws_iam_role_policy_attachment" "cluster_policies" {
  for_each = toset([
    "arn:${local.partition}:iam::aws:policy/AmazonEKSClusterPolicy",
    "arn:${local.partition}:iam::aws:policy/AmazonEKSComputePolicy",
    "arn:${local.partition}:iam::aws:policy/AmazonEKSBlockStoragePolicy",
    "arn:${local.partition}:iam::aws:policy/AmazonEKSLoadBalancingPolicy",
    "arn:${local.partition}:iam::aws:policy/AmazonEKSNetworkingPolicy",
  ])

  role       = aws_iam_role.cluster.name
  policy_arn = each.value

  depends_on = [aws_iam_role.cluster]
}

################################################################################
# EKS Auto Mode Node Role
# Created upfront with ECR pull-through cache permissions so nodes can
# import images from ghcr.io on first pull.
################################################################################

resource "aws_iam_role" "node" {
  name = "${local.cluster_name}-node-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "node_policies" {
  for_each = toset([
    "arn:${local.partition}:iam::aws:policy/AmazonEKSWorkerNodeMinimalPolicy",
    "arn:${local.partition}:iam::aws:policy/AmazonEC2ContainerRegistryPullOnly",
  ])

  role       = aws_iam_role.node.name
  policy_arn = each.value

  depends_on = [aws_iam_role.node]
}

resource "aws_iam_role_policy" "node_ecr_ptc" {
  name = "ECRPullThroughCache"
  role = aws_iam_role.node.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ecr:CreateRepository",
        "ecr:BatchImportUpstreamImage"
      ]
      Resource = "arn:${local.partition}:ecr:${var.region}:${local.account_id}:repository/fluxcd/*"
    }]
  })

  depends_on = [aws_iam_role.node]
}

################################################################################
# EKS Cluster
################################################################################

resource "aws_eks_cluster" "this" {
  name     = local.cluster_name
  version  = local.cluster_version
  role_arn = aws_iam_role.cluster.arn

  vpc_config {
    subnet_ids              = module.vpc.private_subnets
    endpoint_public_access  = true
    endpoint_private_access = true
  }

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = true
  }

  bootstrap_self_managed_addons = false

  compute_config {
    enabled       = true
    node_pools    = ["general-purpose"]
    node_role_arn = aws_iam_role.node.arn
  }

  kubernetes_network_config {
    elastic_load_balancing {
      enabled = true
    }
  }

  storage_config {
    block_storage {
      enabled = true
    }
  }

  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  depends_on = [
    aws_iam_role_policy_attachment.cluster_policies,
    aws_iam_role_policy_attachment.node_policies,
    aws_iam_role_policy.node_ecr_ptc,
  ]

  # ACK manages the cluster configuration after bootstrap.
  # Terraform only creates it; all day-2 changes go through ACK.
  lifecycle {
    ignore_changes = all
  }
}

################################################################################
# Cluster Security Group - Webhook ingress rule
################################################################################

resource "aws_security_group" "prow_webhook_nlb" {
  name        = "prow-webhook-nlb-sg"
  description = "Security group for Prow webhook NLB (GitHub IPs only)"
  vpc_id      = module.vpc.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
    description = "Allow all outbound"
  }

  ingress {
    from_port   = 8888
    to_port     = 8888
    protocol    = "tcp"
    cidr_blocks = ["192.30.252.0/22", "185.199.108.0/22", "140.82.112.0/20"]
    description = "GitHub webhook"
  }

  ingress {
    from_port   = 8888
    to_port     = 8888
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr]
    description = "NLB health checks"
  }

  depends_on = [module.vpc]
}

resource "aws_vpc_security_group_ingress_rule" "webhook_to_cluster" {
  security_group_id            = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
  referenced_security_group_id = aws_security_group.prow_webhook_nlb.id
  from_port                    = 8888
  to_port                      = 8888
  ip_protocol                  = "tcp"
  description                  = "Webhook NLB to Prow hook pods"

  depends_on = [aws_eks_cluster.this, aws_security_group.prow_webhook_nlb]
}

################################################################################
# Cluster Admin Role
################################################################################

resource "aws_iam_role" "cluster_admin" {
  name = "ack-cluster-admin-access-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "cluster_admin_describe" {
  name = "DescribeCluster"
  role = aws_iam_role.cluster_admin.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["eks:DescribeCluster"]
      Resource = aws_eks_cluster.this.arn
    }]
  })

  depends_on = [aws_iam_role.cluster_admin, aws_eks_cluster.this]
}

################################################################################
# Providers (Kubernetes + Helm + kubectl)
################################################################################

provider "kubernetes" {
  host                   = aws_eks_cluster.this.endpoint
  cluster_ca_certificate = base64decode(aws_eks_cluster.this.certificate_authority[0].data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.this.name, "--region", var.region]
  }
}

provider "helm" {
  kubernetes = {
    host                   = aws_eks_cluster.this.endpoint
    cluster_ca_certificate = base64decode(aws_eks_cluster.this.certificate_authority[0].data)

    exec = {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.this.name, "--region", var.region]
    }
  }
}

provider "kubectl" {
  host                   = aws_eks_cluster.this.endpoint
  cluster_ca_certificate = base64decode(aws_eks_cluster.this.certificate_authority[0].data)
  load_config_file       = false

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.this.name, "--region", var.region]
  }
}

################################################################################
# NodePool Swap - delete general-purpose once prow-compute is ready
################################################################################

resource "null_resource" "swap_nodepool" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
    script       = "${path.module}/scripts/swap-nodepool.sh"
  }

  provisioner "local-exec" {
    command = "${self.triggers.script} ${self.triggers.cluster_name} ${self.triggers.region}"
  }

  depends_on = [
    null_resource.validate_kustomizations
  ]
}
