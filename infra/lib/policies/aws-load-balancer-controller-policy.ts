import * as iam from '@aws-cdk/aws-iam';

export const policies: iam.PolicyStatement[] = [
  new iam.PolicyStatement({
    actions: [
      "iam:CreateServiceLinkedRole",
      "ec2:DescribeAccountAttributes",
      "ec2:DescribeAddresses",
      "ec2:DescribeAvailabilityZones",
      "ec2:DescribeInternetGateways",
      "ec2:DescribeVpcs",
      "ec2:DescribeSubnets",
      "ec2:DescribeSecurityGroups",
      "ec2:DescribeInstances",
      "ec2:DescribeNetworkInterfaces",
      "ec2:DescribeTags",
      "ec2:GetCoipPoolUsage",
      "ec2:DescribeCoipPools",
      "elasticloadbalancing:DescribeLoadBalancers",
      "elasticloadbalancing:DescribeLoadBalancerAttributes",
      "elasticloadbalancing:DescribeListeners",
      "elasticloadbalancing:DescribeListenerCertificates",
      "elasticloadbalancing:DescribeSSLPolicies",
      "elasticloadbalancing:DescribeRules",
      "elasticloadbalancing:DescribeTargetGroups",
      "elasticloadbalancing:DescribeTargetGroupAttributes",
      "elasticloadbalancing:DescribeTargetHealth",
      "elasticloadbalancing:DescribeTags",
      "elasticloadbalancing:CreateListener",
      "elasticloadbalancing:DeleteListener",
      "elasticloadbalancing:CreateRule",
      "elasticloadbalancing:DeleteRule"
    ],
    resources: ["*"],
  }),
  new iam.PolicyStatement({
    actions: [
      "cognito-idp:DescribeUserPoolClient",
      "acm:ListCertificates",
      "acm:DescribeCertificate",
      "iam:ListServerCertificates",
      "iam:GetServerCertificate",
      "waf-regional:GetWebACL",
      "waf-regional:GetWebACLForResource",
      "waf-regional:AssociateWebACL",
      "waf-regional:DisassociateWebACL",
      "wafv2:GetWebACL",
      "wafv2:GetWebACLForResource",
      "wafv2:AssociateWebACL",
      "wafv2:DisassociateWebACL",
      "shield:GetSubscriptionState",
      "shield:DescribeProtection",
      "shield:CreateProtection",
      "shield:DeleteProtection"
    ],
    resources: ["*"],
  }),
  new iam.PolicyStatement({
    actions: [
      "ec2:AuthorizeSecurityGroupIngress",
      "ec2:RevokeSecurityGroupIngress"
    ],
    resources: ["*"],
  }),
  new iam.PolicyStatement({
    actions: [
      "ec2:CreateSecurityGroup"
    ],
    resources: ["*"],
  }),
  new iam.PolicyStatement({
    actions: [
      "ec2:CreateTags"
    ],
    resources: ["arn:aws:ec2:*:*:security-group/*"],
    conditions: {
      "StringEquals": {
        "ec2:CreateAction": "CreateSecurityGroup"
      },
      "Null": {
        "aws:RequestTag/elbv2.k8s.aws/cluster": "false"
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "ec2:CreateTags",
      "ec2:DeleteTags"
    ],
    resources: ["arn:aws:ec2:*:*:security-group/*"],
    conditions: {
      "Null": {
        "aws:RequestTag/elbv2.k8s.aws/cluster": "true",
        "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "ec2:AuthorizeSecurityGroupIngress",
      "ec2:RevokeSecurityGroupIngress",
      "ec2:DeleteSecurityGroup"
    ],
    resources: ["*"],
    conditions: {
      "Null": {
        "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:CreateLoadBalancer",
      "elasticloadbalancing:CreateTargetGroup"
    ],
    resources: ["*"],
    conditions: {
      "Null": {
        "aws:RequestTag/elbv2.k8s.aws/cluster": "false",
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:AddTags",
      "elasticloadbalancing:RemoveTags"
    ],
    resources: [
      "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*",
      "arn:aws:elasticloadbalancing:*:*:loadbalancer/net/*/*",
      "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/*/*"
    ],
    conditions: {
      "Null": {
        "aws:RequestTag/elbv2.k8s.aws/cluster": "true",
        "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:AddTags",
      "elasticloadbalancing:RemoveTags"
    ],
    resources: [
      "arn:aws:elasticloadbalancing:*:*:listener/net/*/*/*",
      "arn:aws:elasticloadbalancing:*:*:listener/app/*/*/*",
      "arn:aws:elasticloadbalancing:*:*:listener-rule/net/*/*/*",
      "arn:aws:elasticloadbalancing:*:*:listener-rule/app/*/*/*"
    ],
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:ModifyLoadBalancerAttributes",
      "elasticloadbalancing:SetIpAddressType",
      "elasticloadbalancing:SetSecurityGroups",
      "elasticloadbalancing:SetSubnets",
      "elasticloadbalancing:DeleteLoadBalancer",
      "elasticloadbalancing:ModifyTargetGroup",
      "elasticloadbalancing:ModifyTargetGroupAttributes",
      "elasticloadbalancing:DeleteTargetGroup"
    ],
    resources: ["*"],
    conditions: {
      "Null": {
        "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
      }
    }
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:RegisterTargets",
      "elasticloadbalancing:DeregisterTargets"
    ],
    resources: ["arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"],
  }),
  new iam.PolicyStatement({
    actions: [
      "elasticloadbalancing:SetWebAcl",
      "elasticloadbalancing:ModifyListener",
      "elasticloadbalancing:AddListenerCertificates",
      "elasticloadbalancing:RemoveListenerCertificates",
      "elasticloadbalancing:ModifyRule"
    ],
    resources: ["*"],
  })
];