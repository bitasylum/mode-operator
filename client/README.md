# mode-operator client library

Go packages for programs that read or change a cluster's mode through
mode-operator's `ClusterMode` objects: the mode manager, and programs such as
an orchestration server that move a cluster between modes.

```sh
go get github.com/bitasylum/mode-operator/client
```

It needs Go 1.26 or later and `k8s.io/client-go` 0.37 or later, and depends
on nothing else beyond the Kubernetes API libraries. Releases are tagged
`client/vX.Y.Z`; until the first one, `go get` takes the latest commit.

| Package | Use it to | Depends on |
|---|---|---|
| [`clustermode`](clustermode) | read, watch and change a ClusterMode, and plan changes along the definition's transitions | |
| [`fingerprint`](fingerprint) | take and compare the nodes' boot IDs, keep them in a ConfigMap, and keep that current | |
| [`startup`](startup) | decide which mode the mode manager starts in, and write that decision | both |

Use as much as you need: `clustermode` and `fingerprint` are independent of
each other.

## Changing the mode

Changes are checked against the rules mode-operator copies from the
ClusterModeDefinition into the ClusterMode's status: the modes, the default
mode and the transitions. That copy is what the API server enforces, so a
program needs read access to the ClusterMode only. The library reads the
definition itself only until mode-operator has written the copy, for example
right after the mode manager creates the ClusterMode.

```go
reader, err := clustermode.NewReaderForConfig(cfg, "default")
cm, err := reader.Get(ctx)
rules, err := reader.Rules(ctx, cm)
fmt.Println(rules.Next(cm.Mode)) // the modes a user may choose now
```

- **The mode manager** uses `clustermode.NewModeManagerWriter`. Every write
  sets `spec.mode`, `spec.definition` and the `mode.bitasylum.org/writer`
  label with server-side apply, field manager `mode-manager` and `Force`, and
  creates the ClusterMode if it doesn't exist.
- **Other writers**, such as an orchestration server, use
  `clustermode.NewWriter` with a field manager of their own. They change
  `spec.mode` only and never create the ClusterMode.
- `Writer.Set` checks a change before writing it, and returns an error whose
  `clustermode.ReasonOf` is `UnknownMode`, `TransitionNotAllowed` or
  `NotSettled` when the API server would reject it. `Writer.Apply` writes
  without checking first; when the API server rejects the change, the reason
  comes from re-reading the ClusterMode.
- `Writer.MoveTo` takes as many changes as the transitions need, waiting for
  each mode to settle before leaving it, and calls back after each change so
  the mode manager can broadcast it.
- `Reader.WaitSettled` waits until every workload has settled in a mode,
  which includes their pods having stopped after a scale-down. An
  orchestration server waits for this before powering servers off.
- The mode manager passes every watch event to `Writer.Observe`, which
  reports a change made by anyone else, and which field manager made it, so
  the mode manager can adopt it without writing it back.

## Fingerprints

```go
before, err := fingerprint.Take(ctx, kube)
// ... power cycle the servers ...
after, err := fingerprint.Take(ctx, kube)
diff := fingerprint.Compare(before, after) // Unchanged, Rebooted, Gone, Added
```

The mode manager keeps a fingerprint in a ConfigMap with `fingerprint.Store`,
and a `fingerprint.Keeper` updates it as nodes join, leave and reboot, so a
rolling reboot is never mistaken for a full one. `startup.Run` compares the
stored fingerprint with the nodes' boot IDs on startup: if any node stayed
up, it keeps the stored mode, and otherwise it resets the ClusterMode to its
definition's default mode.

## Access

The roles named here are the ones mode-operator's chart installs.

| Program | Needs |
|---|---|
| a reader | the `mode-operator-clustermode-viewer` role; `Reader.Rules` also needs `mode-operator-clustermodedefinition-viewer` until mode-operator has written the ClusterMode's status |
| another writer | get, list, watch and patch on its ClusterMode only, with `resourceNames` |
| the mode manager | the `mode-operator-clustermode-writer` role: create, get, list, watch, update and patch on its ClusterMode only, and reading ClusterModeDefinitions |
| `fingerprint.Take` | list on Nodes |
| `fingerprint.Keeper` | list and watch on Nodes |
| `fingerprint.Store` | create, get and patch on its own ConfigMap, with `resourceNames` |
| `startup.NewRecorder` | create and patch on Events in the `default` namespace, where events about the cluster-scoped ClusterMode are recorded |

A rule like this gives another writer what it needs:

```yaml
- apiGroups: [mode.bitasylum.org]
  resources: [clustermodes]
  resourceNames: [default]
  verbs: [get, list, watch, patch]
```

## Development

From the repository root, `make test-client` runs the unit tests, and the
integration tests in [integration](integration) against a local API server
with the CRDs, admission policy and roles from `config/`. `make lint` and
`make vulncheck` cover the library too.
