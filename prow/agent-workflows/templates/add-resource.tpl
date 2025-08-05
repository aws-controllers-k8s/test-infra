    ack_resource_workflow:
        description: "ACK resource addition workflow"
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["./prow-job.sh"]
        required_args: ["service", "resource"]
        optional_args: []
        environment:
            GITHUB_ORG: aws-controllers-k8s
            GITHUB_EMAIL_PREFIX: "82905295"
            GITHUB_ACTOR: ack-bot
            MODEL_AGENT_KB_ID: INVALID_ID
        environmentFromSecrets:
            GITHUB_TOKEN:
                name: prowjob-github-pat-token
                key: token
        timeout: "45m"
        resources:
            cpu: "2"
            memory: "4Gi"