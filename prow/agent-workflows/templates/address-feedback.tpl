    address-feedback:
        description: "Address review feedback on an ACK agent pull request"
        # Started by the agent-plugin from a pull_request_review event, never
        # by an /agent command. It shares the add-resource role-harness image.
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["/app/prow-job.sh"]
        required_args: ["service", "resource", "review-id", "review-sha", "team-members"]
        optional_args: ["field", "model", "aws-sdk-version"]
        environment:
            GITHUB_ORG: ${TEST_INFRA_ORG}
            GITHUB_EMAIL_PREFIX: "327606448"
            GITHUB_ACTOR: ack-agent
        environmentFromSecrets:
            GITHUB_TOKEN:
                name: agent-github-pat-token
                key: token
        # Feedback can change behavior the e2e tests cover, so the job tests
        # its changes. The harness skips e2e when nothing changed.
        e2e: true
        timeout: "5h"
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