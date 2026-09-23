    add-field:
        description: "ACK field addition workflow"
        # The field and resource workflows share one role-harness image. Their
        # CLI command selects field-specific prompts, schemas, and reporting.
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["/app/prow-job.sh"]
        required_args: ["service", "resource", "field"]
        optional_args: ["model", "aws-sdk-version"]
        environment:
            GITHUB_ORG: ${TEST_INFRA_ORG}
            GITHUB_EMAIL_PREFIX: "327606448"
            GITHUB_ACTOR: ack-agent
        environmentFromSecrets:
            GITHUB_TOKEN:
                name: agent-github-pat-token
                key: token
        e2e: true
        timeout: "90m"
        resources:
            cpu: "6"
            memory: "10Gi"
        extra_refs:
            - org: aws-controllers-k8s
              repo: code-generator
              base_ref: main
              env: CODEGEN_DIR
            - org: aws-controllers-k8s
              repo: runtime
              base_ref: main
            - org: aws-controllers-k8s
              repo: ack-dev-skills
              base_ref: main
              env: ACK_DEV_SKILLS_DIR
            - org: aws-controllers-k8s
              repo: test-infra
              base_ref: main
              env: TEST_INFRA_DIR
