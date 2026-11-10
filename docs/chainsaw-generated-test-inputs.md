# Writing inputs.yaml for generated Chainsaw tests

`code-generator` generates a Chainsaw e2e test for each resource that has an
`inputs.yaml`. The generator takes everything it can from the resource's model:
the AWS calls that read and delete the resource, which fields are immutable,
whether the resource can be adopted, and how most fields are read back from AWS.
`inputs.yaml` holds the rest:

- valid values for the resource's fields
- the other resources it needs
- how to read back the few fields the model can't explain

`inputs.yaml` is to the tests what `generator.yaml` is to the controller:
configuration for the parts an API's shape can't express.

## Where it goes

```
<service>-controller/
└── test/e2e/chainsaw/          # opts the controller in to Chainsaw tests
    ├── .chainsaw.yaml          # generated: the suite's Chainsaw configuration
    ├── _templates/             # generated: step templates the tests share
    └── <resource>/             # e.g. vpc, internet-gateway, security-group
        ├── inputs.yaml         # written by hand
        └── ...                 # generated; don't edit
```

- `inputs.yaml` is the only file you write. Everything else is generated, and
  starts with a `Code generated … DO NOT EDIT.` header.
- `<resource>` is the CRD's snake_case name with `_` replaced by `-`, e.g.
  `internet_gateway` becomes `internet-gateway`. The generator lists the
  resources that have no `inputs.yaml`.
- To change `.chainsaw.yaml` or a step template for one controller, replace
  it with your own file without the header. The generator keeps a file that
  doesn't start with it.
- When `test/e2e/chainsaw/` exists, `make build-controller SERVICE=<service>`
  regenerates the tests. To regenerate the tests alone:

  ```sh
  ack-generate chainsaw <service> -o ../<service>-controller \
    --aws-sdk-go-version <version> \
    --generator-config-path ../<service>-controller/generator.yaml \
    --metadata-config-path ../<service>-controller/metadata.yaml
  ```

- The e2e runner runs them, instead of the controller's pytest tests, when
  `CHAINSAW_TESTS_ENABLED=true`. In Prow, adding the service to
  `chainsaw_test_services` in test-infra's `prow/jobs/jobs_config.yaml` adds an
  optional `<service>-chainsaw-e2e` job that sets it. pytest keeps running in
  `<service>-kind-e2e`.

## Template

```yaml
# Other resources the resource needs, as full manifests. They're applied before
# the resource and deleted after the test. Refer to them through the resource's
# reference fields, e.g. vpcRef.from.name.
dependencies:
- apiVersion: <group>/<version>
  kind: <Kind>
  metadata:
    name: <name>             # unique among this file's dependencies
  spec:
    <field>: <value>

# Required. The spec of the resource to create. Set every field you want
# tested; the test only checks fields that are set.
create:
  <field>: <value>

# Applied in order. Each update is merged into the spec before it, as a merge
# patch is: maps merge, lists and plain values are replaced, and null removes
# the field. Change every updatable field at least once.
updates:
- <field>: <new value>
- <field>: null

# Other create specs, each with a lifecycle test of its own named
# <resource>-<name>: create, check in AWS, apply its updates, delete. Use them
# for fields that conflict with the main create spec, that only matter at
# create, or to create with only some fields set. They aren't merged with
# create.
variants:
- name: <name>               # lowercase letters, digits and -
  description: <what the variant covers>
  create:
    <field>: <value>
  updates:                   # optional
  - <field>: <new value>

# A spec that the controller accepts but AWS rejects, so the resource must go
# ACK.Terminal. message is a substring of the expected Terminal message.
invalid:
  spec:
    <field>: <bad value>
  message: "<substring of the AWS error>"

# Values worked out when the test runs, after the dependencies are Synced and
# before the resource is created. A value is $NAME in any string of the
# inputs: specs, dependencies, checks. Use them for:
#   - lookups: an AMI ID, an AZ, the account ID
#   - a dependency's AWS ID, for a plain-string field that isn't a reference
#   - AWS resources outside the controller: an S3 bucket, a load balancer
values:
- name: <NAME>               # uppercase letters, digits and _
  # Prints the value. It runs with sh, and can use $SUFFIX, $NAMESPACE,
  # $AWS_REGION and the values before it.
  command: <command>
  # Optional: runs at the end of the test, even after a failure, before the
  # dependencies are deleted. Cleanups run in the reverse order of the values.
  cleanup: <command>

# The error code the read call fails with once the resource is gone. Only
# needed if generator.yaml doesn't declare exceptions.errors.404.code.
notFoundCode: <ErrorCode>

# For a resource AWS keeps describing after it's deleted, e.g. a terminated
# instance: a JMESPath expression over the read call's output that's true once
# it's gone. A not-found error also counts as gone. With neither this nor a
# not-found code, the test can't confirm the deletion in AWS.
deleted: "<JMESPath expression>"

# Only if the controller's adoption code needs more in adoption-fields than
# the resource's identifier (e.g. FlowLog's flowLogID): those spec fields. Each
# must be a string field create sets. The adoption test copies their values
# from the CR it adopts from. For example, FlowLog's adoption hook also needs
# [resourceID, resourceType].
adoptionFields: [<field>, <field>]

# Read-back checks for the fields the generator can't read back by itself. The
# report at the top of the generated chainsaw-test.yaml lists these fields,
# with why. A check here replaces any check the generator derives.
checks:
  # Most fields: where the field is in the resource, as the resource's read
  # call returns it. The test compares it with the spec's value, converted to
  # AWS's names and shape.
  <field>:
    path: <Member>.<Member>        # in AWS's names, e.g. Attachments[0].VpcId
    # For a list: which items to compare, and in what order. All optional.
    where: {<Member>: <value>}     # keep only these items
    exclude: {<Member>: <value>}   # leave out these, e.g. ones AWS adds
    sortBy: <Member>               # or [<Member>, <Member>]
    select: <Member>               # compare one member of each item
    unordered: true                # for a list of plain values: compare it sorted
  # A field the resource's read call doesn't return: an AWS CLI call printing
  # its value as JSON, in the form the CR holds it. $AWS_ID is the resource's
  # AWS ID.
  <other-field>:
    command: >-
      aws <service> <other-call> --<id-flag> $AWS_ID --query '<JMESPath>'
    unordered: true                # sorted, so the command must sort too
    # Only check the field when the spec sets it to this value. This is for
    # fields that only act one way: for example, a flag whose false leaves AWS
    # as it was.
    when: true
```

## Rules

- **Field names are the CR's JSON names, as they appear in the CRD's spec.**
  For example `enableDNSSupport`, not the API's `EnableDnsSupport`. The
  generator rejects a `checks` key that isn't a spec field. It doesn't validate
  `create` and `updates` yet, and the API server silently drops unknown fields.
  Check names against `config/crd/bases/`.
- **Leave immutable fields out of `updates`.** For each immutable field
  `create` sets to a string, bool or number, the generator adds a step that
  changes it and expects the change to be rejected.
- **Don't only test fully specified resources.** Setting every field in
  `create` can hide bugs in how the controller handles unset fields. Add a
  variant that leaves the optional fields unset.
- **AWS names get a suffix, so tests running at once don't clash.** Each test
  has its own random 8 characters, `[a-z0-9]`, as `$SUFFIX`. The generator
  adds `-$SUFFIX` to every spec's `name` by itself: a SecurityGroup's
  `name: ack-sg` is `ack-sg-x7k2m9qp` in AWS, and the checks expect that. Add
  `$SUFFIX` yourself anywhere else an AWS name must be unique: another `*Name`
  field, or a name in a value's command, e.g. an S3 bucket's
  `ack-bucket-$SUFFIX`. It works in any string in the inputs, and in commands
  it's an environment variable. The report notes `*Name` fields set without
  it. CR names don't need it: every test runs in a namespace of its own.
- **Give values AWS accepts.** The test creates real resources, so use
  private CIDRs, and names valid for the resource.
- **The `invalid` spec must get past the CRD's own validation.** It has to be
  rejected by AWS, which makes the resource Terminal. The API server rejects a
  spec that doesn't fit the CRD's schema before the controller sees it, so
  `kubectl apply` fails, and so does the test's create step. For VPC:

  | Spec | Result |
  |---|---|
  | `cidrBlocks: "10.0.0.0/16"`, a string, not a list | API server: `spec.cidrBlocks in body must be of type array` |
  | No `cidrBlocks` | API server: `spec.cidrBlocks: Required value` |
  | `cidrBlocks: [InvalidValue]` | Accepted, then AWS rejects it: the resource goes Terminal. Use this kind. |

  Check a candidate with `kubectl apply --dry-run=server -f <manifest>`.
- **A `checks` command runs once, and must print the settled value.** Checks
  run once the CR is Synced, or once an update has been reconciled, when the
  controller has just read the resource from AWS. They aren't retried, apart
  from the AWS CLI's own retries of throttling and transient errors. So print
  the settled value, not a transitional one: for example, filter CIDR blocks
  to `associated` ones.
- **Prefer `path` to `command`.** A `path` needs no AWS CLI call or JMESPath:
  the test reads the resource once, and compares what's at the path with the
  spec's value. A map only has to hold the keys the spec sets, so AWS can
  return more. A list must have the same length and order, so use `exclude`
  for items AWS adds, e.g. RouteTable's `local` route, and `sortBy` for a
  list whose order AWS doesn't keep. A JSON document, e.g. a policy, is
  compared by content.
- **A check's command must print one JSON value.** It runs in a shell script
  with `AWS_DEFAULT_OUTPUT=json` set. Single-quote the `--query`, and quote
  JMESPath literals as JSON: `` `"associated"` ``, `` `0` ``.
- **A value's command must print only the value.** Send anything else to
  `/dev/null` or stderr. A value is set only once its command succeeds and
  prints something. If a command creates a resource and then fails, its
  cleanup doesn't run. Tag what it creates
  `services.k8s.aws/namespace=$NAMESPACE`, as the controller tags what it
  creates, so a leftover can be traced to its test.
- **Point references at dependencies by name**, e.g.
  `vpcRef: {from: {name: igw-vpc}}`. A reference is checked in AWS through the
  field it fills: `vpcRef` through `vpcID`, expecting the `igw-vpc` VPC's
  `status.vpcID`. The test reads that ID from the dependency's CR when the
  check runs. If the read call returns the ID somewhere else, declare a
  check for the reference field itself, e.g. InternetGateway's
  `vpcRef: {path: Attachments[0].VpcId}`. A `command` for a list of
  references, e.g. `subnetRefs`, must print the IDs sorted. References inside
  a field's value work the same way, wherever `generator.yaml` declares them:
  in RouteTable's `routes`, each route's `gatewayRef` is expected as the
  `gatewayID` it fills. References with `from.namespace` aren't checked.

## What the generator does with the inputs

Each resource gets these tests:

- **`<resource>`, the lifecycle test:**
  1. Create the dependencies and the resource. If `invalid` is set, also create
     the invalid resource and check it goes Terminal.
  2. Wait for the resource to be Synced, then check every set field in AWS.
  3. Try to change each immutable field, and check each change is rejected.
  4. For each update, apply it, wait for the controller to reconcile it, and
     check every set field in AWS again.
  5. Delete the resource, and check it's gone from AWS.
- **`<resource>-<variant>`, for each variant:** the same lifecycle, for the
  variant's create spec and updates. There's no invalid spec.
- **`<resource>-adoption`, if the resource can be adopted:**
  1. Create the resource through a CR with `deletion-policy: retain`, then
     delete that CR.
  2. Adopt the resource left behind into a new CR, by its identifier (e.g.
     `flowLogID`), plus any `adoptionFields`.
  3. Check the fields in AWS, and check the adopted CR's spec was filled in
     from AWS. A string field holding a JSON document, such as a policy, is
     compared by content, because AWS can return it with its keys reordered.
  4. Delete the resource.

The generator reads a field back from AWS in one of these ways, trying them in
this order:

1. Your entry in `checks`.
2. The resource's read call, when the field's AWS name is in its output.
   Renames in `generator.yaml` are followed. A reference field is read back
   through the field it fills, e.g. `vpcRef` through `vpcID`.
3. For a field from a write call in `generator.yaml` (`from: operation`), the
   matching read call. For example, `ModifyVpcAttribute` →
   `DescribeVpcAttribute --attribute enableDnsSupport`, and `PutBucketCors` →
   `GetBucketCors`.
4. For a `custom_field` holding a list of an AWS structure, the read output's
   member holding a list of that structure. For example, SecurityGroup's
   `ingressRules` → `IpPermissions`.
5. Otherwise, the field is listed as unchecked, with the reason.

ACK's own `services.k8s.aws/*` tags are checked separately from the spec's
tags.

## Reading the report

Each generated `chainsaw-test.yaml` starts with a report. `ack-generate` also
prints it.

| Section | Meaning | What to do |
|---|---|---|
| Create variants | The variants, each a test of its own | Nothing |
| Checked in AWS | The fields the test reads back | Nothing |
| Not checked in AWS | The fields `create` or `updates` set that the test can't read back, with why | Add an entry to `checks` |
| Not immutable, but no update changes them | Spec fields not marked `is_immutable` that no update changes | Add an update, or mark the field `is_immutable` in `generator.yaml` if the controller can't update it |
| Immutable, tried to change / not tried | The immutable fields the test tries to change, and those it can't, such as lists | Nothing, or test the skipped ones by hand |
| Notes | Other gaps, e.g. no `invalid` spec, no not-found code, an update the checks can't see | Fill in the missing input |

## Example: VPC
The generator reads back `instanceTenancy`, `tags` and the three DNS/NAU attributes by
itself. `cidrBlocks` is a `custom_field` of plain strings, and
`disallowSecurityGroupDefaultRules` is a hook-only field, so both need checks.

```yaml
create:
  cidrBlocks: [10.0.0.0/16, 10.2.0.0/16]
  instanceTenancy: default
  enableDNSSupport: true
  enableDNSHostnames: true
  enableNetworkAddressUsageMetrics: true
  disallowSecurityGroupDefaultRules: false
  tags:
  - key: initialtagkey
    value: initialtagvalue
updates:
# Every updatable field changes
- cidrBlocks: [10.0.0.0/16]
  enableDNSSupport: false
  enableNetworkAddressUsageMetrics: false
  disallowSecurityGroupDefaultRules: true
  tags:
  - key: updatedtagkey
    value: updatedtagvalue
# What the first can't do: a second CIDR change (10.2.0.0/16 may still be
# disassociating, so a new block), reverts, and removing the tags
- cidrBlocks: [10.0.0.0/16, 10.3.0.0/16]
  enableDNSHostnames: false
  disallowSecurityGroupDefaultRules: false
  tags: []
variants:
# Only matters at create, and with the attributes left unset: that's the case
# that found a controller bug
- name: disallowed
  description: Default security group rules removed at create, with no other attributes set
  create:
    cidrBlocks: [10.0.0.0/16]
    disallowSecurityGroupDefaultRules: true
invalid:
  spec:
    cidrBlocks: [InvalidValue]
  message: "InvalidParameterValue: Value (InvalidValue) for parameter cidrBlock is invalid. This is not a valid CIDR block."
checks:
  # The VPC's associated blocks, of all those it's had
  cidrBlocks:
    path: CidrBlockAssociationSet
    where: {CidrBlockState.State: associated}
    select: CidrBlock
    unordered: true
  # true removes the default security group's rules. false doesn't put them
  # back, so only true can be checked.
  disallowSecurityGroupDefaultRules:
    command: >-
      aws ec2 describe-security-groups
      --filters Name=vpc-id,Values=$AWS_ID Name=group-name,Values=default
      --query 'length(SecurityGroups[0].IpPermissions) == `0` && length(SecurityGroups[0].IpPermissionsEgress) == `0`'
    when: true
```

## Example: a resource with a dependency

InternetGateway depends on a VPC, and attaches to it through `vpcRef`. AWS
returns the VPC under `Attachments`, so `vpcRef` needs a check:

```yaml
dependencies:
- apiVersion: ec2.services.k8s.aws/v1alpha1
  kind: VPC
  metadata:
    name: igw-vpc
  spec:
    cidrBlocks: [10.0.0.0/16]
create:
  tags:
  - key: initialtagkey
    value: initialtagvalue
updates:
# Attach to the VPC
- vpcRef:
    from:
      name: igw-vpc
  tags:
  - key: updatedtagkey
    value: updatedtagvalue
# Detach
- vpcRef: null
  tags: []
# Not declared in generator.yaml
notFoundCode: InvalidInternetGatewayID.NotFound
checks:
  vpcRef:
    path: Attachments[0].VpcId
```

## Example: values for something outside the controller

VPCEndpointServiceConfiguration needs a Network Load Balancer, which isn't an
ec2 resource. A value creates one in the dependency subnet, and its cleanup
deletes it before the subnet is deleted:

```yaml
dependencies:
- apiVersion: ec2.services.k8s.aws/v1alpha1
  kind: VPC
  metadata:
    name: vpces-vpc
  spec:
    cidrBlocks: [10.0.0.0/16]
- apiVersion: ec2.services.k8s.aws/v1alpha1
  kind: Subnet
  metadata:
    name: vpces-subnet
  spec:
    cidrBlock: 10.0.1.0/24
    vpcRef:
      from:
        name: vpces-vpc
values:
- name: ACCOUNT_ID
  command: aws sts get-caller-identity --query Account --output text
- name: SUBNET_ID
  command: kubectl get subnet vpces-subnet -n $NAMESPACE -o jsonpath='{.status.subnetID}'
- name: NLB_ARN
  command: >-
    arn=$(aws elbv2 create-load-balancer --name ack-chainsaw-$SUFFIX
    --type network --scheme internal --subnets $SUBNET_ID
    --tags Key=services.k8s.aws/namespace,Value=$NAMESPACE
    --query 'LoadBalancers[0].LoadBalancerArn' --output text) &&
    aws elbv2 wait load-balancer-available --load-balancer-arns "$arn" &&
    echo "$arn"
  cleanup: >-
    aws elbv2 delete-load-balancer --load-balancer-arn $NLB_ARN &&
    aws elbv2 wait load-balancers-deleted --load-balancer-arns $NLB_ARN
create:
  networkLoadBalancerARNs:
  - $NLB_ARN
  allowedPrincipals:
  - arn:aws:iam::$ACCOUNT_ID:root
  # ...
```

Instance uses a value for its AMI, and `deleted` because a terminated instance
stays in `describe-instances` for about an hour:

```yaml
values:
- name: AMI_ID
  command: >-
    aws ssm get-parameter
    --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64
    --query Parameter.Value --output text
create:
  imageID: $AMI_ID
deleted: "Reservations[0].Instances[0].State.Name == 'terminated'"
```

## Not supported yet

- **Validating `create` and `updates` against the CRD schema.**
- **Adoption beyond the basics:** adopting a resource created outside ACK,
  updating it after adoption, and the adopt-or-create policy.
- **Checking that a Synced resource stops calling update**, i.e. that it has
  no perpetual diff.
