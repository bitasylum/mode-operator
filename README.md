# mode-operator

A Kubernetes operator that scales opted-in Deployments, StatefulSets and
KubeVirt VMs to the replica count each one declares for the cluster's current
mode. The mode lives in a `ClusterMode` object; the modes allowed are defined
in a `ClusterModeDefinition`.

The design is in the parent repo:
[ship-core-mode-controller/docs/design.md](https://github.com/Audiobuzz/ship-core-mode-controller/blob/main/docs/design.md).
Section 9.1 describes this repo's layout.

Status: not yet scaffolded.
