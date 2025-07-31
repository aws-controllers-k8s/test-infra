    ack_resource_workflow:
        description: "ACK resource addition workflow"
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["./prow-job.sh"]
        required_args: ["service", "resource"]
        optional_args: []
        environment:
            GITHUB_ORG: ack-prow-staging
            GITHUB_EMAIL_PREFIX: 219906516
            MODEL_AGENT_KB_ID: WN5I1BIMGT
        environmentFromSecrets:
            GITHUB_TOKEN:
                name: tamer-github-token
                key: token
            GITHUB_ACTOR:
                name: tamer-github-actor
                key: actor
        timeout: "30m"
        resources:
            cpu: "2"
            memory: "4Gi"