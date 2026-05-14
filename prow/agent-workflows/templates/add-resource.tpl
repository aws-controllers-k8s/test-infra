    add-resource:
        description: "ACK resource addition workflow"
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["./prow-job.sh"]
        required_args: ["service", "resource"]
        optional_args: ["model", "aws-sdk-version"]
        environment:
            GITHUB_ORG: aws-controllers-k8s
            GITHUB_EMAIL_PREFIX: "82905295"
            GITHUB_ACTOR: ack-bot
            JOBS_CONFIG_PATH: "/prow/jobs/jobs_config.yaml"
        secretVolumes:
            - secretName: prowjob-github-pat-token
              key: github-pat-token
              envVar: GITHUB_TOKEN
            - secretName: api-model-kb
              key: api-model-kb
              envVar: MODEL_AGENT_KB_ID
        timeout: "45m"
        resources:
            cpu: "2"
            memory: "4Gi"