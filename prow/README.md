# Prow configuration

## `/config`

Contains the Helm chart to configure Prow on the test infrastructure cluster.
Any changes to the chart will automatically be applied by the Flux2 HelmRelease
located in the `../flux` directory.

## `/jobs`

Contains all of the Prow jobs configured to run as part of the CI and CD systems
for any ACK service controller.