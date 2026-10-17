# mode-operator

A Kubernetes operator that scales opted-in Deployments, StatefulSets and
KubeVirt VMs to the replica count each one declares for the cluster's current
mode. The mode lives in a `ClusterMode` object; the modes allowed are defined
in a `ClusterModeDefinition`.

The design is in the parent repo:
[ship-core-mode-controller/docs/design.md](https://github.com/Audiobuzz/ship-core-mode-controller/blob/main/docs/design.md).
Section 9.1 describes this repo's layout.

Status:
- **Done:**
  - the `ClusterMode` and `ClusterModeDefinition` APIs and their validation
  - the Deployment and StatefulSet reconcilers, which apply the current mode,
    including the scale-to-zero delay
  - `ClusterMode` status (workload states, counts, conditions) and the
    Prometheus metrics
- **Next:** the KubeVirt kinds, then the Helm chart.

## Development

```sh
make test   # unit tests and envtest suites (downloads API server binaries once)
make lint
```
