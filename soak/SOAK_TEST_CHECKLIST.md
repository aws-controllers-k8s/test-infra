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

Record the outcome of every check below. For each item capture **four** things so
the conclusion is reproducible and auditable:

- **What we looked at** — the specific metric / log signal.
- **Command run** — the exact command or query used to obtain it (`$SERVICE` = service alias).
- **Observed** — the value(s) returned.
- **Why it passes/fails** — how the observed value maps to the pass criteria.

The quick-glance table records the verdict; the per-check blocks below it record
the evidence.

### Verdict table

| # | Check | Required | Result (PASS/FAIL) |
|---|-------|----------|--------------------|
| 1 | No panics / restarts | ✅ | |
| 2 | No reconcile errors | ✅ | |
| 3 | No infinite loops | ✅ | |
| 4 | No leaks (mem/goroutine/fd) | ✅ | |
| 5 | e2e all passing | ✅ | |
| 6 | No 5xx / unexpected 4xx | ✅ | |
| 7 | Latency stable | ⬜ | |
| 8 | No dangling resources | ⬜ | |
| 9 | CPU bounded | ⬜ | |
| 10 | API volume reasonable | ⬜ | |

**Overall: PASS only if all required (✅) checks pass for the entire run.**

### Evidence log (fill one block per check)

```
#1 No panics / restarts                                          [PASS/FAIL]
  Looked at: reconcile/webhook panic counters; controller pod restart count
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=controller_runtime_reconcile_panics_total'
             curl -s 'http://localhost:9090/api/v1/query?query=controller_runtime_webhook_panics_total'
             kubectl get pods -n ack-system
             kubectl logs -n ack-system <controller-pod> | grep -ci panic
  Observed:  <panic counters = ?> ; <RESTARTS = ?> ; <panic log lines = ?>
  Conclusion: PASS if all counters/restarts/log hits = 0.

#2 No reconcile errors                                           [PASS/FAIL]
  Looked at: controller_runtime_reconcile_errors_total (counter + rate)
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=controller_runtime_reconcile_errors_total{controller="<resource>"}'
             curl -s 'http://localhost:9090/api/v1/query?query=sum(rate(controller_runtime_reconcile_errors_total[5m]))by(controller)'
  Observed:  <error counter = ?> ; <peak error rate = ?>
  Conclusion: PASS if counter = 0 (or transient blip that returns to 0 and resources still Synced).

#3 No infinite reconcile loops                                   [PASS/FAIL]
  Looked at: reconcile-rate shape over the run; workqueue depth + longest-running processor
  Command:   # rate must decay to ~0 after the e2e runner stops
             curl -s 'http://localhost:9090/api/v1/query_range?query=sum(rate(controller_runtime_reconcile_total{controller="<resource>"}[5m]))&start=<run_start>&end=<now>&step=1800'
             curl -s 'http://localhost:9090/api/v1/query?query=workqueue_depth{name="<resource>"}'
             curl -s 'http://localhost:9090/api/v1/query?query=workqueue_longest_running_processor_seconds{name="<resource>"}'
  Observed:  <peak rate = ?/s> ; <tail rate after run = ?> ; <queue depth = ?> ; <longest running = ?s>
  Conclusion: PASS if rate decays to ~0, depth returns to 0, longest-running ~0.
              (workqueue_retries_total ≈ reconcile count is EXPECTED — requeue_after re-adds.)

#4 No leaks (memory / goroutine / fd)                            [PASS/FAIL]
  Looked at: RSS, heap objects, goroutines, OS threads, open fds — as TRENDS, not points
  Command:   # hourly trend across the full run for each series (job=ack-controller-$SERVICE)
             curl -s 'http://localhost:9090/api/v1/query_range?query=process_resident_memory_bytes{job="ack-controller-'$SERVICE'"}&start=<run_start>&end=<now>&step=3600'
             # repeat for: go_memstats_heap_objects, go_goroutines, go_threads, process_open_fds
  Observed:  RSS first/last/min/max = ? ; goroutines first/last = ? ; threads = ? ; fds = ?
  Conclusion: PASS if RSS/heap are flat or saw-tooth (GC reclaims) with no monotonic ramp,
              and goroutines/threads/fds are stable. Guard: end RSS within ~10-15% of post-warmup baseline.

#5 e2e all passing                                               [PASS/FAIL]
  Looked at: pytest summary lines across every iteration in the runner log
  Command:   kubectl logs -n ack-system job/$SERVICE-soak-test > /tmp/$SERVICE-soak.log
             grep -cE '[0-9]+ passed'         /tmp/$SERVICE-soak.log   # iterations that passed
             grep -cE '[1-9][0-9]* failed'     /tmp/$SERVICE-soak.log   # MUST be 0
             grep -cE '[1-9][0-9]* error'      /tmp/$SERVICE-soak.log   # MUST be 0
             # (or via Loki: {namespace="ack-system",pod=~"<service>-soak-test.*"} |~ "failed|ERROR")
  Observed:  <passed iterations = ?> ; <failed = ?> ; <errors = ?>
  Conclusion: PASS if failed = 0 AND errors = 0 for the whole window.

#6 No 5xx / unexpected 4xx AWS faults                            [PASS/FAIL]
  Looked at: ack_outbound_api_requests_error_total by op_id + fault (status_code is a FAULT ENUM:
             1=server/5xx-class, 2=client/4xx-class — NOT an HTTP code; dashboard 4../5.. panels are blind)
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=ack_outbound_api_requests_error_total{status_code="1"}'  # server faults
             curl -s 'http://localhost:9090/api/v1/query?query=ack_outbound_api_requests_error_total{status_code="2"}'  # client faults
             curl -s 'http://localhost:9090/api/v1/query?query=ack_outbound_api_requests_total'                          # for cross-check vs create cycles
  Observed:  <status_code=1 series/counts = ?> ; <status_code=2 by op = ?>
  Conclusion: PASS if status_code="1" = 0, and every status_code="2" is an expected read-before-create
              lookup miss (e.g. Get<Resource>) whose count tracks create cycles. Any client fault on a
              mutating op (Create/Update/Delete/Tag) is a FAIL.

#7 Reconcile latency stable (recommended)                        [PASS/FAIL]
  Looked at: reconcile time p90 / p100
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=max_over_time((histogram_quantile(0.9,sum by(le)(rate(controller_runtime_reconcile_time_seconds_bucket{controller="<resource>"}[5m])))*1000)[2d:5m])'
             # p100: replace 0.9 with 1.0
  Observed:  <p90 peak = ? ms> ; <p100 peak = ? ms>
  Conclusion: PASS if p90 stable with no growing tail (compare to prior release if available).

#8 No dangling resources (recommended)                           [PASS/FAIL]
  Looked at: count of ACK objects in the apiserver over the run
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=max_over_time(apiserver_storage_objects{resource=~".*services.k8s.aws"}[2d])'
  Observed:  <max count per resource = ?>
  Conclusion: PASS if count returns to baseline between iterations (no monotonic climb).

#9 CPU bounded (recommended)                                     [PASS/FAIL]
  Looked at: controller pod CPU cores
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=max_over_time(sum(rate(container_cpu_usage_seconds_total{namespace="ack-system",pod=~"soak-test-'$SERVICE'.*"}[5m]))[2d:5m])'
  Observed:  <peak cores = ?>
  Conclusion: PASS if CPU tracks load and idles down (no pegged-at-limit plateau).

#10 API volume reasonable (recommended)                          [PASS/FAIL]
  Looked at: total outbound AWS calls by op + overall rate
  Command:   curl -s 'http://localhost:9090/api/v1/query?query=ack_outbound_api_requests_total'
             curl -s 'http://localhost:9090/api/v1/query?query=sum(rate(ack_outbound_api_requests_total[5m]))'
  Observed:  <grand total = ?> ; <per-op breakdown = ?>
  Conclusion: PASS if counts scale with e2e iterations; no single op exploding disproportionately.
```

> Tip: with the Prometheus port-forward active (`kubectl port-forward -n
> prometheus-$SERVICE service/kube-prom-$SERVICE-kube-promet-prometheus
> 9090:9090`), use `http://localhost:9090` for IPv4 or `http://[::1]:9090` if the
> forward only binds IPv6.

---

## Worked example — `ack-soak-glue` (24h run, PASS)

A known-good run, with the evidence log filled in. Use it to calibrate
expectations for a healthy single-resource controller (`job` is the glue resource).

### Verdict table

| # | Check | Required | Result |
|---|-------|----------|--------|
| 1 | No panics / restarts | ✅ | PASS |
| 2 | No reconcile errors | ✅ | PASS |
| 3 | No infinite loops | ✅ | PASS |
| 4 | No leaks | ✅ | PASS |
| 5 | e2e all passing | ✅ | PASS |
| 6 | No 5xx / unexpected 4xx | ✅ | PASS |
| 7 | Latency stable | ⬜ | PASS |
| 8 | No dangling resources | ⬜ | PASS |
| 9 | CPU bounded | ⬜ | PASS |
| 10 | API volume reasonable | ⬜ | PASS |

**Overall: PASS**

### Evidence log

```
#1 No panics / restarts                                          [PASS]
  Looked at: reconcile/webhook panic counters; controller pod restarts
  Command:   curl .../query?query=controller_runtime_reconcile_panics_total
             curl .../query?query=controller_runtime_webhook_panics_total
             kubectl get pods -n ack-system
  Observed:  reconcile_panics_total=0, webhook_panics_total=0; controller pod RESTARTS=0 (up 47h)
  Conclusion: all zero -> PASS

#2 No reconcile errors                                           [PASS]
  Looked at: controller_runtime_reconcile_errors_total
  Command:   curl .../query?query=controller_runtime_reconcile_errors_total{controller="job"}
  Observed:  job=0, fieldexport=0
  Conclusion: zero errors -> PASS

#3 No infinite reconcile loops                                   [PASS]
  Looked at: reconcile-rate shape; workqueue depth/longest-running
  Command:   curl .../query_range?query=sum(rate(controller_runtime_reconcile_total{controller="job"}[5m]))...
             curl .../query?query=workqueue_depth{name="job"}
  Observed:  reconcile_total = 6138 success + 6138 requeue_after, 0 error; rate peaked 0.144/s and
             fell to 0.000 after the runner finished; workqueue_depth{name="job"}=0; longest_running=0s
  Conclusion: rate decays to 0, queue drained -> PASS (retries_total≈6138 is the expected requeue_after)

#4 No leaks (memory / goroutine / fd)                            [PASS]
  Looked at: RSS, heap objects, goroutines, threads, fds as trends (job=ack-controller-glue)
  Command:   curl .../query_range?query=process_resident_memory_bytes{job="ack-controller-glue"}&step=3600
             (repeat for go_memstats_heap_objects, go_goroutines, go_threads, process_open_fds)
  Observed:  RSS 63.0 -> 64.0 MiB (min 63 / max 67.3 over 48h); heap_objects 44k-95k saw-tooth;
             goroutines 79 -> 78; os_threads 8 (flat); open_fds 10-11 (flat)
  Conclusion: no monotonic ramp anywhere -> PASS

#5 e2e all passing                                               [PASS]
  Looked at: pytest summary lines across all iterations
  Command:   kubectl logs -n ack-system glue-soak-test-<pod> > glue-soak.log
             grep -cE '1 passed' glue-soak.log    -> 3061
             grep -cE '[1-9][0-9]* failed' glue-soak.log -> 0
             grep -cE '[0-9]+ error' glue-soak.log -> 0
  Observed:  3061 iterations, all "1 passed"; 0 failed; 0 errors
  Conclusion: zero failures across 3061 iterations -> PASS

#6 No 5xx / unexpected 4xx AWS faults                            [PASS]
  Looked at: ack_outbound_api_requests_error_total by op + fault enum
  Command:   curl .../query?query=ack_outbound_api_requests_error_total
             curl .../query?query=ack_outbound_api_requests_total
  Observed:  status_code="1" (server): none. status_code="2" (client): only GetJob = 3069,
             which equals the CreateJob/DeleteJob count (read-before-create not-found).
             Total calls 33,759: GetJob 12276, ListTagsForResource 9207, Create/Update/Delete/TagJob 3069 each.
  Conclusion: no server faults; only expected GetJob not-found client faults -> PASS

#7 Reconcile latency stable                                      [PASS]
  Command:   curl .../query?query=max_over_time((histogram_quantile(1.0,...reconcile_time..._bucket{controller="job"}...))*1000)[2d:5m])
  Observed:  p100 peak 700 ms; no growing tail
  Conclusion: bounded -> PASS

#8 No dangling resources                                         [PASS]
  Command:   curl .../query?query=max_over_time(apiserver_storage_objects{resource=~".*services.k8s.aws"}[2d])
  Observed:  jobs.glue.services.k8s.aws peaked at 1; fieldexports/iamroleselectors = 0
  Conclusion: at most 1 resource at a time, no accumulation -> PASS

#9 CPU bounded                                                   [PASS]
  Command:   curl .../query?query=max_over_time(sum(rate(container_cpu_usage_seconds_total{namespace="ack-system",pod=~"soak-test-glue.*"}[5m]))[2d:5m])
  Observed:  peak 0.0024 cores; idled after run
  Conclusion: nowhere near a limit -> PASS

#10 API volume reasonable                                        [PASS]
  Command:   curl .../query?query=ack_outbound_api_requests_total
  Observed:  33,759 calls scaling with iterations; GetJob highest (read-heavy reconcile), no anomaly
  Conclusion: proportional to load -> PASS
```
