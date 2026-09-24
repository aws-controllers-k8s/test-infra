    add-resource:
        description: "ACK resource addition workflow"
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        # Absolute path: with extra_refs, Prow's decoration runs the entrypoint
        # from a clonerefs checkout dir, not the image's /app, so a relative
        # "./prow-job.sh" would not resolve.
        command: ["/app/prow-job.sh"]
        required_args: ["service", "resource"]
        optional_args: ["model", "aws-sdk-version"]
        environment:
            GITHUB_ORG: ${TEST_INFRA_ORG}
            GITHUB_EMAIL_PREFIX: "219906516"
            GITHUB_ACTOR: ack-test-agent
        environmentFromSecrets:
            GITHUB_TOKEN:
                name: agent-github-pat-token
                key: token
        e2e: true
        timeout: "90m"
        resources:
            cpu: "6"
            memory: "10Gi"
        # Stable repo dependencies mounted into the pod by Prow's clonerefs init
        # container. The service controller is NOT listed here — prow-job.sh forks
        # and clones it dynamically per run. `env` injects each ref's checkout path
        # so the workflow reads exactly where clonerefs placed the repo.
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