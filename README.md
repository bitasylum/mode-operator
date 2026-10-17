# mode-operator

mode-operator scales Deployments and StatefulSets to the replica count each
one declares for the cluster's current mode. A mode is a name such as
`normal`, `peak` or `standby`. A separate mode manager decides which mode the
cluster is in and writes it to a `ClusterMode` object. mode-operator applies
it to every workload that has opted in, and reports in the `ClusterMode`'s
status how far the change has got.

The design, with the reasoning behind each choice, is in the parent repo:
[ship-core-mode-controller/docs/design.md](https://github.com/Audiobuzz/ship-core-mode-controller/blob/main/docs/design.md).

**Status:** Deployments and StatefulSets are supported. KubeVirt
VirtualMachines and VirtualMachinePools are planned for the next release; the
API and the annotations won't change for them.

## How it works

| Object | Written by | Holds |
|---|---|---|
| `ClusterModeDefinition` (cluster-scoped) | admins, usually through Git | the modes allowed, and the mode to start in |
| `ClusterMode` (cluster-scoped) | the mode manager; maintainers by hand | which definition to follow, and the current mode |
| `ClusterMode` status | mode-operator only | each workload's state, counts, replica sums and conditions |
| a workload's label and annotations | the workload's owner, in Git | which ClusterMode it follows, and its replicas in each mode |

The API server rejects a mode that isn't in the definition. mode-operator
copies the definition's modes into `status.modes`, and the CRD checks every
change of `spec.mode` against that copy.

## Requirements

- Any conformant Kubernetes cluster, version 1.30 or later. Nothing in the
  chart or the manifests is specific to a distribution; kind is only used by
  the tests.
- Helm 4 (tested with 4.3) or Kustomize, to install. Helm 3.8 or later should
  work, but isn't tested.

## Install

```sh
kubectl create namespace mode-operator-system
kubectl label namespace mode-operator-system pod-security.kubernetes.io/enforce=restricted
helm install mode-operator ./dist/chart --namespace mode-operator-system
```

- **No image is published yet.** Until a release is, build and push your own
  with `make docker-build docker-push IMG=<registry>/mode-operator:<tag>` and
  install with `--set image.repository=<registry>/mode-operator --set
  image.tag=<tag>`.
- **Use the release name `mode-operator`.** Objects are named
  `<release>-<component>` (with the chart name added if the release name
  doesn't contain it), so this gives the names used here and expected by the
  mode manager's chart, such as `mode-operator-clustermode-writer`. With
  another release name, set `fullnameOverride: mode-operator`.
- One install per cluster: the CRDs and the controller are cluster-wide.

The chart installs both CRDs, the controller (2 replicas with leader
election), its RBAC, the roles for people and for the mode manager described
under [Access](#access), and the admission policy described under
[Transitions](#transitions). The main values:

| Value | Default | Meaning |
|---|---|---|
| `image.repository`, `image.tag` | `ghcr.io/audiobuzz/mode-operator`, the chart's appVersion | the controller image |
| `controller.replicas` | `2` | replicas; one leads at a time |
| `controller.settleTimeout` | `10m` | how long a workload may take to reach its replicas before it counts as `Stalled` |
| `controller.extraArgs` | `[]` | more flags, for example `--zap-log-level=debug` |
| `crds.install`, `crds.keep` | `true`, `true` | install the CRDs, and leave them in place on uninstall |
| `definitions` | `[]` | ClusterModeDefinitions to install with the chart |
| `modeManager.clusterModes` | `[default]` | the ClusterModes the mode manager may write |
| `rbac.maintainers` | none | `groups` and `serviceAccounts` allowed to change the mode by hand |
| `rbac.viewers`, `rbac.definitionAdmins` | none | `groups` and `serviceAccounts` for the read-only roles and the definition-admin role |
| `metrics.certSecret` | `""` | a TLS Secret to serve metrics with, instead of a self-signed certificate |
| `metrics.serviceMonitor.enabled` | `false` | create a Prometheus Operator ServiceMonitor |
| `networkPolicy.enabled` | `false` | allow metrics traffic only from labelled namespaces |

See [dist/chart/values.yaml](dist/chart/values.yaml) for the rest.

**Definitions in the chart need two steps with plain Helm.** `helm install`
checks every object's kind before it creates anything, so it can't create a
ClusterModeDefinition in the same release as its CRD. Install the chart
without `definitions` first, then add them with `helm upgrade`. Argo CD can do
it in one sync: the chart marks definitions with `SkipDryRunOnMissingResource`.
Definitions stay in place when you remove them from `definitions`; delete
one by hand if you mean to.

To install with Kustomize instead, `make deploy IMG=<image>` applies
`config/default`, and `kubectl apply -k config/mode-manager/rbac` adds the
mode manager's writer role. Read [CRDs and uninstalling](#crds-and-uninstalling)
before you use `make undeploy`.

## Quick start

1. Define the modes:
   ```yaml
   apiVersion: mode.bitasylum.org/v1alpha1
   kind: ClusterModeDefinition
   metadata:
     name: standard
   spec:
     modes: [normal, peak, offpeak, maintenance, standby]
     defaultMode: standby
   ```
2. Install the mode manager. It creates ClusterMode `default` and writes the
   mode. On a test cluster you can create it by hand instead, from
   [config/samples/mode_v1alpha1_clustermode.yaml](config/samples/mode_v1alpha1_clustermode.yaml).
3. Opt a workload in, as in
   [config/examples/app-deployment.yaml](config/examples/app-deployment.yaml).
4. Watch it:
   ```console
   $ kubectl get clustermode
   NAME      MODE     APPLIED      READY        WORKLOADS   SETTLED   AGE
   default   normal   AllApplied   AllSettled   12          12        3d
   ```

## Opting a workload in

Put the label and annotations on the workload's own metadata, not on its pod
template, which would roll the pods whenever they changed.

| Key | Kind | Meaning |
|---|---|---|
| `mode.bitasylum.org/clustermode` | label | opts in, and names the ClusterMode to follow |
| `mode.bitasylum.org/replicas` | annotation | `mode=N` entries, comma-separated, for example `normal=3,peak=6,offpeak=1`; `*=N` sets the count for modes that aren't listed; a mode that isn't listed, with no `*`, gets 0 |
| `mode.bitasylum.org/scale-to-zero-delay` | annotation, optional | a Go duration such as `5m` to wait before scaling to 0 |
| `mode.bitasylum.org/settle-timeout` | annotation, optional | a Go duration: how long the workload may take to reach its replicas before it counts as `Stalled`; overrides `--settle-timeout` |

- **Every mode named must be in the definition** that the workload's
  ClusterMode uses. A typo such as `peek=6` makes the workload `Invalid`, and
  it keeps its current replicas rather than scaling to its `*` or zero count.
- **The scale-to-zero delay only holds a scale to 0.** It never delays a
  scale-up, or a scale-down to a count above 0. Going back to a mode with
  replicas during the wait cancels the scale-down. Moving between two modes
  that both want 0 doesn't restart the wait. The start time is stored on the
  workload, so the wait survives restarts.
- **An invalid delay** holds a scale to 0 until it's fixed, and is reported
  at once, even while the current mode doesn't want 0.
- **Set `replicas: 0` in Git.** Argo CD uses it only when it creates the
  workload, and mode-operator sets the real count straight after.
- **Don't opt in a workload that an HPA scales.** Both would write
  `spec.replicas`.

mode-operator writes three annotations of its own: `applied-mode`,
`zero-pending-since` and `replicas-changed-at`, all under
`mode.bitasylum.org/`. Don't put them in Git.

## Modes and definitions

- **Adding a mode:** add it to the definition first, then use it in the mode
  manager and in workload annotations. No release of mode-operator is needed.
- **Removing a mode:** stop using it in the mode manager and in workload
  annotations first, then remove it from the definition. A workload whose
  annotation still names it becomes `Invalid` and keeps its replicas. A
  ClusterMode that is in the removed mode reports `InvalidMode`, and its
  workloads are left as they are until the mode changes.
- **A ClusterMode's definition can't be changed** once set.

### Transitions

A definition can also say which mode may follow which, making the modes a
state machine:

```yaml
spec:
  modes: [normal, peak, offpeak, maintenance, standby]
  defaultMode: standby
  transitions:
    - from: normal
      to: [peak, offpeak]
    - from: peak
      to: [normal]
    - from: offpeak
      to: [normal]
    - from: standby
      to: [normal, maintenance]
```

With `transitions` set, the API server accepts a change of `spec.mode` only
if:

- the rule for the current mode lists the new one; a mode with no rule, such
  as `maintenance` above, can only be left for the default mode; and
- the current mode is `Ready`, in a status mode-operator computed from the
  latest spec: every workload that isn't `Invalid` was `Settled`,
  `status.mode` is the current mode, and `status.observedGeneration` matches
  `metadata.generation`.

**Changing to `defaultMode` is always allowed**, whatever the current mode
and however far it has got, so the mode manager can reset after a full
cluster reboot, and a maintainer has a way out when a workload is stuck.
Here, peak reaches maintenance by way of standby: the move to standby is
always allowed, and the move on to maintenance waits until standby is
`Ready`. Without `transitions`, any mode may follow any other, at any time.

- **The default mode needs a rule**, or a ClusterMode that resets to it could
  never leave it, and `transitions` can't be an empty list. The API server
  rejects both.
- **With the definition deleted, or the ClusterMode in `InvalidMode`,**
  `Ready` is False, so only the default mode can be reached.
- **mode-operator copies the rules** into the ClusterMode's
  `status.transitions` and `status.defaultMode`, which is what the API server
  checks against. Until mode-operator has first written the status of a new
  ClusterMode, changes aren't checked.
- **The up-to-date check is an admission policy,** `mode-operator-transitions`,
  which the chart and `config/default` install; the CRD's own rules can't
  read `metadata.generation`. Without it, a ClusterMode moved to the default
  mode and back before mode-operator writes status could leave on the `Ready`
  from before.
- **Whenever mode-operator isn't writing status**, while it is down or after
  a full cluster restart until its first status write, the last status it
  wrote counts, even if the workloads have changed since. The mode manager's
  reset to the default mode after a restart doesn't change the generation if
  the ClusterMode is already in it.

To get a ClusterMode out of a stuck mode:

1. Change to the default mode, which is always allowed.
2. If the default mode can't become `Ready` either, remove `transitions` from
   the definition. mode-operator clears `status.transitions` and the
   restrictions go.
3. If mode-operator isn't running, a cluster admin can clear the copy by
   hand: `kubectl patch clustermode default --subresource=status --type=json
   -p '[{"op":"remove","path":"/status/transitions"}]'`. mode-operator copies
   it back from the definition when it next runs, so do step 2 as well.

## Changing the mode by hand

Maintainers can override the mode:

```sh
kubectl patch clustermode default --type=merge -p '{"spec":{"mode":"maintenance"}}'
```

The API server rejects a mode that isn't in the definition, and a change the
definition's transitions don't allow. The mode manager
adopts the new mode and carries on from it; it may change the mode again
later as usual. `kubectl get clustermode -o wide` shows the field manager
that set the mode in the `SET-BY` column, for example `kubectl-patch`. The
client chooses that name, so it says which tool was used, not who used it;
the API server's audit log records the user.

## ClusterMode status

`kubectl get clustermode` shows the mode, the reason of the `Applied` and
`Ready` conditions, and the workload counts. `-o wide` adds the replica sums,
who set the mode, and the definition. `kubectl get clustermode default -o
yaml` shows the full status, including up to 20 workloads that aren't
settled, most urgent first, each with a message.

Every workload is in exactly one state:

| State | Meaning |
|---|---|
| `Invalid` | an annotation error stops mode-operator from applying the mode; replicas are left as they are |
| `Pending` | waiting out its scale-to-zero delay |
| `Applying` | mode-operator hasn't written the mode's replicas yet; normally seconds |
| `Stalled` | hasn't reached its replicas within its settle timeout |
| `Scaling` | its replicas are set, and its pods aren't all ready yet, or some are still to stop |
| `Settled` | its replicas are set and reached |

A workload that falls short again after settling, for example during an
image rollout, gets a fresh settle timeout.

A workload is `Settled` only while none of its pods is still to stop, so after
a scale-down `Ready` means the old pods have gone and released what they
used, not just stopped being counted.

- **Each pod can take up to its `terminationGracePeriodSeconds` to stop** (30
  seconds by default), and a StatefulSet with `podManagementPolicy:
  OrderedReady` stops its pods one at a time. Give such workloads a
  `settle-timeout` longer than that.
- **For a Deployment, any pod shutting down counts,** not only after a
  scale-down: an evicted pod, or an old pod during a rollout. Deployments
  report them in `status.terminatingReplicas`, a beta field on by default in
  recent Kubernetes; on a cluster without it, a Deployment's scale-down
  settles once the count drops.
- **A StatefulSet counts the pods above its count** until they have gone, and
  any other pod once it is no longer ready.
- **A pod stuck on a node that has gone down** keeps either kind unsettled
  until the node comes back, its Node object is deleted, or someone
  force-deletes the pod. The workload goes `Stalled`, the ClusterMode
  `Degraded`, and with `transitions` only the default mode can be reached
  until then.

| Condition | True when | Reasons |
|---|---|---|
| `Applied` | every workload that isn't `Invalid` has the mode's replicas | `AllApplied`, `NoWorkloads`; False: `Applying`, `ScaleToZeroPending` |
| `Ready` | every workload that isn't `Invalid` is `Settled` | `AllSettled`, `NoWorkloads`; False: `Stalled`, `Applying`, `Scaling`, `ScaleToZeroPending` |
| `Degraded` | a workload has an annotation error or is `Stalled`, or the mode can't be applied at all | `InvalidWorkloads`, `StalledWorkloads`, `InvalidAndStalledWorkloads`, `DefinitionNotFound`, `InvalidMode`; False: `AsExpected` |

A pending scale to zero makes `Applied` and `Ready` False by design, because
pods are still running. Alert on `Degraded`, not on `Ready`.

## For the mode manager

The mode manager's side of the contract is in section 6 of the design. In
short:

- Write `spec.mode`, `spec.definition` and the label
  `mode.bitasylum.org/writer` with server-side apply, field manager
  `mode-manager` and `Force`, in every apply. Create the ClusterMode if it
  doesn't exist.
- Write the mode before broadcasting it, use only modes from the definition,
  and never write `status`.
- If the definition has `transitions`, move only along them, and only once
  `status.observedGeneration` equals `metadata.generation`, `status.mode` is
  the current mode and the `Ready` condition is True. Plan paths of several
  steps itself, for example peak → standby → maintenance with the rules
  under [Transitions](#transitions). A reset to `defaultMode` is always
  allowed.
- Run one writer at a time (`strategy: Recreate` while it has one replica).
- Bind its ServiceAccount to `mode-operator-clustermode-writer`, which allows
  only the ClusterModes in the chart's `modeManager.clusterModes`.
- On startup, restore the mode if a node stayed up while it was down, and
  otherwise start in the definition's `defaultMode`. A full cluster reboot
  therefore resets the mode, for example to `standby`.

## Argo CD

1. Merge [config/argocd/argocd-cm-patch.yaml](config/argocd/argocd-cm-patch.yaml)
   into `argocd-cm`. It tells Argo CD to ignore the fields mode-operator owns.
2. Give every Application with workloads that follow a ClusterMode
   `syncOptions: [RespectIgnoreDifferences=true, ServerSideApply=true]`, as in
   [config/examples/argocd-application.yaml](config/examples/argocd-application.yaml).
   Without `RespectIgnoreDifferences`, every sync writes Git's replicas over
   mode-operator's.
3. Keep the label and the user annotations in Git, and `replicas: 0`. Keep
   mode-operator's own annotations out of Git, and don't keep ClusterModes in
   Git: the mode manager owns them.
4. Never add a global `jsonPointers: [/spec/replicas]` ignore. It would
   freeze replicas for every workload.
5. If Argo CD installs the chart: Argo CD renders it with `helm template`, so
   Helm's `resource-policy: keep` has no effect there. The CRDs and
   definitions also carry Argo CD's `Prune=false,Delete=false` sync options,
   so deleting the Application, or turning off `crds.install`, leaves them in
   place.

If your Argo CD doesn't honour `managedFieldsManagers` during sync, use an
`ignoreDifferences` entry per Application instead, scoped by workload name,
with `jsonPointers: [/spec/replicas]`.

## Access

| ClusterRole | For | Allows |
|---|---|---|
| `mode-operator-controller` | mode-operator | read ClusterModes and definitions; write ClusterMode status; patch Deployments and StatefulSets; write events |
| `mode-operator-clustermode-maintainer` | maintainers (`rbac.maintainers.groups`) | read ClusterModes and change their mode; read definitions. No create, delete or status |
| `mode-operator-clustermode-viewer` | anyone who needs to see the mode | read ClusterModes and their status |
| `mode-operator-clustermodedefinition-viewer` | anyone who needs to see the modes | read definitions |
| `mode-operator-clustermodedefinition-admin` | admins or the GitOps account that manages modes | full access to definitions |
| `mode-operator-clustermode-writer` | the mode manager | create ClusterModes; read and write only those named in `modeManager.clusterModes`; read definitions. No status |
| `mode-operator-metrics-reader` | Prometheus | read `/metrics` |

Neither mode-operator nor the mode manager can write definitions, and
mode-operator can't write ClusterMode `spec`.

## Metrics and alerts

The metrics endpoint serves HTTPS on port 8443 and checks each caller's token
and RBAC: bind your Prometheus ServiceAccount to
`mode-operator-metrics-reader`. With `metrics.serviceMonitor.enabled`, the
chart creates a ServiceMonitor. The controller's certificate is self-signed,
so by default the ServiceMonitor skips verification. To verify it, set
`metrics.certSecret` to a TLS Secret, for example one issued by cert-manager,
and `metrics.serviceMonitor.tlsConfig` to the CA to check it with.

| Metric | Labels | Meaning |
|---|---|---|
| `mode_operator_current_mode` | `clustermode`, `mode` | 1 for the current mode |
| `mode_operator_mode_changes_total` | `clustermode` | mode changes seen |
| `mode_operator_workloads` | `clustermode`, `state` | workloads in each state |
| `mode_operator_replicas` | `clustermode`, `type` (`desired`, `applied`, `ready`) | replica sums over workloads that aren't `Invalid` |
| `mode_operator_invalid_annotations` | `clustermode` | workloads with any annotation error |
| `mode_operator_condition` | `clustermode`, `type` | 1 when the condition is True, 0 when False |

Reconcile errors come from controller-runtime's
`controller_runtime_reconcile_errors_total`. Suggested alerts:

- `mode_operator_condition{type="Degraded"} == 1` for 15m
- `mode_operator_workloads{state="stalled"} > 0` for 5m
- `mode_operator_invalid_annotations > 0` for 15m, which catches a bad delay
  before it blocks anything
- `rate(controller_runtime_reconcile_errors_total{controller=~"mode-operator-.*"}[5m]) > 0` for 10m

## Restarts and failures

- **mode-operator restarts or fails over:** the new leader picks up where the
  old one stopped. Pending scale-downs keep their start time, because it is
  stored on the workload.
- **A ClusterMode is deleted, or a workload names one that doesn't exist:**
  workloads keep their current replicas, and get a `ClusterModeNotFound`
  warning event.
- **The definition is missing:** the ClusterMode reports `DefinitionNotFound`,
  and its workloads are left as they are.
- **The mode manager restarts:** it restores the mode, unless every node
  rebooted while it was down, in which case it resets to the definition's
  `defaultMode`. On a single-node cluster, any reboot of the node is a full
  reboot.
- **A workload is deleted and recreated:** it starts at Git's `replicas: 0`
  until mode-operator applies the mode, normally within seconds.

## Offboarding a workload

Removing the label stops mode-operator from managing the workload, but its
ownership of `spec.replicas` stays recorded, and Argo CD keeps ignoring
replicas. To hand replicas back to Git:

1. In Git, set `spec.replicas` to the count you want and remove the label.
   Sync.
2. Clear the workload's field ownership:
   `kubectl patch <kind> <name> --type=merge -p '{"metadata":{"managedFields":[{}]}}'`.
3. Sync again. Argo CD now applies the replica count from Git.

Doing step 2 while Git still says `replicas: 0` scales the workload to zero.

## CRDs and uninstalling

**Deleting a CRD deletes every object of its kind.** Deleting the ClusterMode
CRD deletes every ClusterMode; the mode manager recreates its ClusterMode
once the CRD is back, but if it restarts before then, it starts in its
default mode.

- `helm uninstall` leaves both CRDs, every ClusterModeDefinition installed
  through `definitions`, and every ClusterMode in place. Reinstalling with the
  same release name in the same namespace adopts them again; another name
  fails with an ownership error.
- Under Argo CD, the same objects survive deleting the Application.
- `helm upgrade` upgrades the CRDs.
- `make undeploy` and `make uninstall` delete the CRDs, and with them every
  ClusterMode and definition. Use them only on test clusters.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--settle-timeout` | `10m` | how long a workload may take to reach its replicas before it counts as `Stalled` |
| `--leader-elect` | `false` (the chart sets `true`) | elect one replica to reconcile |
| `--metrics-bind-address` | `0` (off; the chart sets `:8443`) | where to serve metrics |
| `--metrics-secure` | `true` | serve metrics over HTTPS with authentication |
| `--health-probe-bind-address` | `:8081` | where to serve `/healthz` and `/readyz` |
| `--zap-devel`, `--zap-log-level` | off, `info` | readable console logs, and the log level |

Logs are JSON by default, and sampled: above 100 identical messages a
second, only 1 in 100 is written. During a mode change that scales hundreds
of workloads at once, some `Applied mode` lines are left out, but every
scale is still recorded as an Event on its workload. `--zap-devel` turns
sampling off.

## Development

```sh
make test        # unit tests and envtest suites (downloads API server binaries once)
make lint
make vulncheck   # govulncheck, under the Go version the image is built with
make helm-lint   # lint the chart and render it with its main options
make chart-check # fail if dist/chart is out of date with config/
make test-e2e    # build the image and run the end-to-end tests on their own kind cluster
make test-chart  # install the chart on a new kind cluster: install, upgrade, a mode change, uninstall, reinstall
```

The kind-based tests use kubeconfigs of their own, so they leave your current
kubectl context alone, and `make test-chart` refuses to run if its cluster
name is taken.

`make manifests` regenerates the CRDs and RBAC from the Go markers and copies
them, and the rules of every role in `config/`, into the chart with
`hack/sync-chart.sh`. The rest of the chart is maintained by hand.

## License

Apache 2.0. See [LICENSE](LICENSE).
