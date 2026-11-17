    triage:
        description: "ACK Autopilot triage, mode api: one needs-config work item to one closed-enum decision (no tools, no GitHub token)"
        # Started by the ACK Autopilot Dispatcher (Plan 8). A team member's
        # `/agent triage` run is harmless: the pod gets no GitHub token, writes
        # nothing, and the Dispatcher reconciles only runs it launched.
        image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "add-resource") }}
        command: ["/app/triage-job.sh"]
        required_args: ["service", "resource", "field", "finding-class", "detector-work", "change-class", "sdk-to", "work-item"]
        optional_args: ["model"]
        environment:
            GITHUB_ORG: ${TEST_INFRA_ORG}
        timeout: "30m"
        resources:
            cpu: "1"
            memory: "2Gi"