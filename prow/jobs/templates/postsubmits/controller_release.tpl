  {{ range $_, $service := .Config.AWSServices  }}
  aws-controllers-k8s/{{ $service }}-controller:
  - name: {{ $service }}-post-submit
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    - org: aws-controllers-k8s
      repo: code-generator
      base_ref: main
      workdir: false
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "deploy") }}
          resources:
            limits:
              cpu: 8
              memory: "5120Mi"
            requests:
              cpu: 2
              memory: "5120Mi"
          securityContext:
            privileged: true
          env:
          - name: GOLANG_VERSION
            value: "1.22.5"
          command: ["/bin/bash", "-c", "cd cd/scripts && ./release-controller.sh"]
    branches: #supports tags too.
    - ^v[0-9]+\.[0-9]+\.[0-9]+$
  {{ if contains $.Config.SoakTestOnReleaseServiceNames $service }}
  - name: {{ $service }}-soak-on-release
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "soak-test") }}
          resources:
            limits:
              cpu: 2
            requests:
              cpu: 2
              memory: "1024Mi"
          securityContext:
            privileged: true
          command: ["/bin/bash", "-c", "cd soak/prow/scripts && ./soak-on-release.sh"]
    branches: #supports tags too.
    - ^v[0-9]+\.[0-9]+\.[0-9]+$
  {{ end }}
  - name: {{ $service }}-controller-release-tag
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "controller-release-tag") }}
          resources:
            limits:
              cpu: 1
              memory: "500Mi"
            requests:
              cpu: 1
              memory: "500Mi"
          command: ["/bin/bash", "-c", "./cd/auto-generate/controller-release-tag.sh"]
    branches:
    - main
  - name: {{ $service }}-controller-olm-bundle-pr
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    - org: aws-controllers-k8s
      repo: code-generator
      base_ref: main
      workdir: false
    - org: aws-controllers-k8s
      repo: runtime
      base_ref: main
      workdir: false
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "olm-bundle-pr") }}
          resources:
            limits:
              cpu: 4
              memory: "3072Mi"
            requests:
              cpu: 4
              memory: "3072Mi"
          command: ["/bin/bash", "-c", "./cd/olm/olm-bundle-pr.sh"]
    branches:
    - ^v[0-9]+\.[0-9]+\.[0-9]+$
  
  - name: update-ack-chart
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    - org: aws-controllers-k8s
      repo: code-generator
      base_ref: main
      workdir: false
    - org: aws-controllers-k8s
      repo: ack-chart
      base_ref: main
      workdir: false
    {{ range $_, $otherService := $.Config.AWSServices}}{{ if ne $otherService $service }}- org: aws-controllers-k8s
      repo: {{ $otherService }}-controller
      base_ref: main
      workdir: false
    {{ end }}{{ end }}spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "deploy") }}
          resources:
            limits:
              cpu: 2
              memory: "2048Mi"
            requests:
              cpu: 2
              memory: "2048Mi"
          command: ["/bin/bash", "-c", "cd cd/ack-chart && ./update-chart.sh"]
    branches:
    - ^v[0-9]+\.[0-9]+\.[0-9]+$
    {{ end }}

