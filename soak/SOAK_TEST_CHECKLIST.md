# ACK Soak Test Validation Checklist

Use this checklist to decide whether a controller **passed** its soak run. A soak
test passes only when every **required** item below is satisfied for the full
test window (default 24h). Each item lists the exact signal to inspect, the
PromQL/LogQL query, and the pass criteria.

> Soak tests introduce load by running the controller's e2e suite continuously.
> All metrics are scraped into the per-service Prometheus
> (`kube-prom-<service>`) and logs into Loki (`loki-<service>`). See
> [README.md](README.md) for how to reach the dashboard.

## How to query

```bash
SERVICE=<service>   # e.g. glue

# Refresh kubeconfig (endpoint changes if the cluster was recreated)
aws eks update-kubeconfig --name ack-soak-$SERVICE --region us-west-2

# Confirm the run finished and the controller never restarted
kubectl get jobs -n ack-system | grep $SERVICE          # expect Complete 1/1
kubectl get pods -n ack-system                          # expect RESTARTS = 0

# Port-forward Prometheus for ad-hoc PromQL
kubectl port-forward -n prometheus-$SERVICE \
  service/kube-prom-$SERVICE-kube-promet-prometheus 9090:9090 &
# then: curl -s 'http://localhost:9090/api/v1/query?query=<expr>'
```

The controller scrape job label is `job="ack-controller-<service>"` (not
`ack-controller`). Filter ACK Go-runtime/process metrics by that job; otherwise
you pick up the kube-apiserver's own `workqueue_*`/`go_*` series.

---

## Required checks

### 1. No controller panics
- **Why:** A panic indicates a hard bug; crashloops invalidate the run.
- **Signals & queries:**
  ```promql
  controller_runtime_reconcile_panics_total
  controller_runtime_webhook_panics_total
  max_over_time(kube_pod_container_status_restarts_total{namespace="ack-system"}[7d])
  ```
  Also: `kubectl get pods -n ack-system` (RESTARTS column).
- **Pass:** all panic counters `= 0` AND controller pod restarts `= 0`.
- **Also check logs:** `{namespace="ack-system",container="controller"} |= "panic"` returns nothing.

### 2. No reconcile errors
- **Why:** Persistent reconcile errors mean the controller can't converge.
- **Query:**
  ```promql
  controller_runtime_reconcile_errors_total{controller=~"<resource>|fieldexport"}
  # rate form to spot sustained errors:
  sum(rate(controller_runtime_reconcile_errors_total[5m])) by (controller)
  ```
- **Pass:** error counter `= 0` (or, if a transient blip exists, the rate returns
  to 0 and resources still reach `Synced` — confirm via e2e results in check 5).

### 3. No infinite reconcile loops
- **Why:** A resource that requeues forever burns API quota and never settles.
- **Signals & queries:**
  ```promql
  # Reconcile rate should track test activity, then fall to ~0 when the e2e
  # runner finishes. A flat, non-zero plateau after the run = stuck loop.
  sum(rate(controller_runtime_reconcile_total{controller="<resource>"}[5m]))

  # Queue must drain; nothing should stay "in progress".
  workqueue_depth{name="<resource>"}
  workqueue_longest_running_processor_seconds{name="<resource>"}
  ```
- **Pass:** reconcile rate decays to ~0 after the e2e runner stops; `workqueue_depth`
  returns to 0; `longest_running_processor_seconds` ~ 0.
- **Note:** `workqueue_retries_total{name="<resource>"}` will roughly equal the
  reconcile count because successful reconciles return `requeue_after`. That is
  **expected** — judge loops by the *rate shape* and *queue depth*, not the raw
  retries counter.

### 4. No memory / goroutine / fd leaks
- **Why:** Slow growth over a long run is the whole point of soaking.
- **Queries (filter by `job="ack-controller-<service>"`):**
  ```promql
  process_resident_memory_bytes          # RSS
  go_memstats_heap_inuse_bytes
  go_memstats_heap_objects               # should oscillate with GC, not ramp
  go_goroutines                          # goroutine leak
  go_threads                             # OS thread leak
  process_open_fds                       # file-descriptor leak
  ```
  Evaluate the **trend** over the run (use `query_range`, hourly step), not a
  point value.
- **Pass:** RSS and heap are flat or saw-tooth (GC reclaims); no monotonic upward
  ramp. `go_goroutines`, `go_threads`, `process_open_fds` stable (no steady climb).
  A useful guard: end-of-run RSS within ~10-15% of the post-warmup baseline.

### 5. All e2e tests passing in the runner logs
- **Why:** The soak load *is* the e2e suite; failures mean functional breakage.
- **Where:** test-runner pod / Loki.
  ```bash
  kubectl logs -n ack-system job/<service>-soak-test | grep -E "passed|failed|error"
  ```
  ```logql
  {namespace="ack-system", pod=~"<service>-soak-test.*"} |~ "failed|ERROR"
  ```
- **Pass:** every pytest iteration ends with `N passed` and **0 failed** for the
  whole window. Any `failed` line must be root-caused (correlate its timestamp
  with the controller log stream — see README "Investigating test failures").

### 6. No AWS API server-side errors (5xx) and no unexpected client errors (4xx)
- **Why:** 5xx = AWS-side/throttling/instability; unexpected 4xx = malformed
  requests or permission/logic bugs.
- **Queries:**
  ```promql
  # All outbound calls + errors, broken down by op and fault.
  ack_outbound_api_requests_total
  ack_outbound_api_requests_error_total
  ```
- **IMPORTANT — read the `status_code` label correctly.** For aws-sdk-go-v2
  controllers the runtime records the **smithy fault enum**, not the HTTP status:
  `0 = Unknown`, `1 = Server (5xx-class)`, `2 = Client (4xx-class)`.
  - The dashboard's "AWS 4xx" / "AWS 5xx" panels filter `status_code=~'4..'`/`'5..'`
    and therefore **show nothing** for v2 controllers. Do not rely on them.
  - Query the fault values instead:
    ```promql
    ack_outbound_api_requests_error_total{status_code="1"}   # server faults -> investigate
    ack_outbound_api_requests_error_total{status_code="2"}   # client faults -> classify
    ```
- **Pass:**
  - `status_code="1"` (server faults): `= 0`, or only rare transient throttling
    that the controller retried successfully (resources still reach `Synced`).
  - `status_code="2"` (client faults): every op must be an **expected** lookup
    miss (e.g. `Get<Resource>` returning entity-not-found during the read-before-
    create step, whose count tracks the number of create cycles). Any client fault
    on a mutating op (Create/Update/Delete/Tag) is a **fail**.

---

## Recommended (non-blocking) checks

### 7. Reconcile latency has not regressed
```promql
histogram_quantile(0.90, rate(controller_runtime_reconcile_time_seconds_bucket{controller="<resource>"}[5m]))*1000   # p90 ms
histogram_quantile(1.00, rate(controller_runtime_reconcile_time_seconds_bucket{controller="<resource>"}[5m]))*1000   # p100 ms
```
Pass: p90 stable across the run; no growing tail. Compare against the previous
release's soak if available.

### 8. No dangling AWS resources / object accumulation
```promql
apiserver_storage_objects{resource=~".*\\.services\\.k8s\\.aws"}
```
Pass: ACK object count returns to baseline between e2e iterations (the runner
creates and deletes). A monotonic climb means resources aren't being cleaned up.

### 9. CPU is bounded
```promql
sum(rate(container_cpu_usage_seconds_total{namespace="ack-system",pod=~"soak-test-<service>.*"}[5m]))
```
Pass: CPU tracks load and idles down after the run; no pegged-at-limit plateau.

### 10. AWS call volume is reasonable
```promql
ack_outbound_api_requests_total                       # by op_type / op_id
sum(rate(ack_outbound_api_requests_total[5m]))
```
Pass: call counts scale with the number of e2e iterations; no single op exploding
disproportionately (a sign of redundant Describe/List churn).

---

## Pass/Fail summary template

| # | Check | Required | Result | Evidence |
|---|-------|----------|--------|----------|
| 1 | No panics / restarts | ✅ | | panic counters, RESTARTS |
| 2 | No reconcile errors | ✅ | | `reconcile_errors_total` |
| 3 | No infinite loops | ✅ | | reconcile-rate shape, queue depth |
| 4 | No leaks (mem/goroutine/fd) | ✅ | | RSS / goroutines / fds trend |
| 5 | e2e all passing | ✅ | | runner logs / Loki |
| 6 | No 5xx / unexpected 4xx | ✅ | | `*_error_total` by fault |
| 7 | Latency stable | ⬜ | | reconcile p90/p100 |
| 8 | No dangling resources | ⬜ | | `apiserver_storage_objects` |
| 9 | CPU bounded | ⬜ | | container CPU rate |
| 10 | API volume reasonable | ⬜ | | `api_requests_total` |

**Overall: PASS only if all required (✅) checks pass for the entire run.**

---

## Reference baseline — `ack-soak-glue` (24h run, healthy)

A known-good run for calibration:

- Pod restarts: **0** (controller up 47h)
- `reconcile_panics_total` / `webhook_panics_total`: **0**
- `reconcile_errors_total{controller="job"}`: **0**
- Reconcile: 6138 success + 6138 requeue_after; rate peaked 0.144/s, fell to 0 after run
- RSS: 63 → 64 MiB (min 63 / max 67 over 48h) — flat
- goroutines 79→78, os_threads 8, open_fds 10-11 — flat
- AWS API: 33,759 calls total (GetJob, ListTagsForResource, Create/Update/Delete/TagJob)
- AWS errors: only `GetJob status_code="2"` (client fault = expected not-found before create), count == create cycles
