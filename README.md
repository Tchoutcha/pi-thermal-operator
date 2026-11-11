# pi-thermal-operator

[![Tests](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/test.yml/badge.svg)](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/test.yml)
[![Lint](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/lint.yml)
[![E2E](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/Tchoutcha/pi-thermal-operator/actions/workflows/test-e2e.yml)

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
- [x] `ThermalPolicy` CRD (configurable thresholds, validated by the API server)
- [x] Prometheus metrics and Kubernetes Events
- [ ] Unit and envtest tests, Helm chart, multi-arch CI
- [ ] Experiment: throttling episodes and pod latency with vs without the operator

## Configure thresholds
Thresholds live in a cluster-scoped `ThermalPolicy` named `default`. Changes apply immediately
to all nodes, without restarting the controller. Invalid values are rejected by the API server.

```bash
make install   # install the CRD
kubectl apply -f config/samples/thermal_v1alpha1_thermalpolicy.yaml
kubectl patch thermalpolicy default --type merge -p '{"spec":{"hotCelsius":75,"coolCelsius":68}}'
kubectl get tp
```
Without a policy, the controller falls back to 75 °C / 68 °C.

## Observability
Every taint change emits a Kubernetes Event on the node:
```
$ kubectl describe node pi-worker3
Events:
  Warning  ThermalTaintAdded  pi-thermal-operator  temperature 56.8 °C >= 55 °C: taint thermal.stellat.dev/hot added
```

Prometheus metrics exposed by the controller:

| Metric | Type | Description |
|---|---|---|
| `pi_thermal_node_temperature_celsius{node}` | gauge | SoC temperature reported by the agent |
| `pi_thermal_node_hot{node}` | gauge | 1 if the node has the thermal taint |
| `pi_thermal_taint_changes_total{action}` | counter | taints `added` / `removed` |
