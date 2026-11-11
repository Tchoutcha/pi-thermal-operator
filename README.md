# pi-thermal-operator

A Kubernetes operator that makes the scheduler **thermal-aware** on Raspberry Pi 5 clusters.
Hot nodes get a `PreferNoSchedule` taint, so new pods prefer cooler nodes; the taint is removed
once the node cools down.

Built and tested on a real 14-node ARM64 cluster (Raspberry Pi 5, K3s).

## Why
Edge boards like the Raspberry Pi 5 throttle their CPU when they get hot. Kubernetes does not see
temperature, so it keeps placing pods on nodes that are already slowing down.

## How it works
```
 Each Pi (DaemonSet agent)                 Controller
 ┌──────────────────────────────┐          ┌───────────────────────────────┐
 │ every 15 s, reads:           │  node    │ temp >= 75 °C → add taint     │
 │ - SoC temperature (sysfs)    │ ───────▶ │ temp <= 68 °C → remove taint  │
 │ - throttling flags (firmware │ annot.   │ in between    → no change     │
 │   mailbox, /dev/vcio)        │          │ (hysteresis)                  │
 └──────────────────────────────┘          └───────────────────────────────┘
```
- **Agent** (`cmd/agent`): reads `/sys/class/thermal/thermal_zone0/temp` and the firmware
  `GET_THROTTLED` tag through `/dev/vcio`, without `vcgencmd`. Writes node annotations:
  `thermal.stellat.dev/temp-celsius`, `thermal.stellat.dev/throttled`, `thermal.stellat.dev/throttled-now`.
- **Controller** (`internal/controller`, kubebuilder / controller-runtime): watches Nodes and
  manages the `thermal.stellat.dev/hot:PreferNoSchedule` taint.

## Quick start
```bash
# 1. Deploy the agent (DaemonSet on arm64 nodes)
kubectl apply -f config/agent/agent.yaml

# 2. Check readings
kubectl get nodes -o custom-columns='NODE:.metadata.name,TEMP:.metadata.annotations.thermal\.stellat\.dev/temp-celsius,THROTTLED:.metadata.annotations.thermal\.stellat\.dev/throttled'

# 3. Run the controller (thresholds can be overridden)
THERMAL_HOT_C=75 THERMAL_COOL_C=68 make run
```

## Validation
- Agent readings match `vcgencmd get_throttled` on real nodes, including non-zero flags
  (`0xe0000` thermal limiting, `0x50000` under-voltage).
- Taint added above the hot threshold, removed below the cool threshold, unchanged in between.

## Roadmap
- [ ] `ThermalPolicy` CRD (configurable thresholds)
- [ ] Prometheus metrics and Kubernetes Events
- [ ] Unit and envtest tests, Helm chart, multi-arch CI
- [ ] Experiment: throttling episodes and pod latency with vs without the operator
