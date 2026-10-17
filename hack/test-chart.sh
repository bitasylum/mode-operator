#!/usr/bin/env bash
# Installs the Helm chart on a new Kind cluster and checks that it works:
# the controller starts, a definition can be added, a workload follows a
# ClusterMode, uninstalling leaves the CRDs and every object in place, and a
# reinstall adopts them again.
#
# The cluster is reached only through a kubeconfig of its own, so your
# current kubectl context is left alone. The script refuses to run if a
# cluster named CHART_TEST_CLUSTER already exists, and deletes only the
# cluster it created, unless KEEP_CLUSTER=1.
#
# Uses IMG (built with make docker-build), HELM, KIND and KUBECTL.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
CLUSTER=${CHART_TEST_CLUSTER:-mode-operator-test-chart}
IMG=${IMG:?set IMG to the image to test}
HELM=${HELM:-helm}
KIND=${KIND:-kind}
KUBECTL=${KUBECTL:-kubectl}
NS=mode-operator-system

KUBECONFIG=$(mktemp)
export KUBECONFIG
created=0

k() { "$KUBECTL" "$@"; }
h() { "$HELM" "$@"; }
step() { echo "== $*"; }
fail() {
  echo "FAILED: $*" >&2
  exit 1
}

# diagnose prints what is needed to debug a failed run, before the cluster
# goes.
diagnose() {
  echo "== diagnostics"
  k -n "$NS" get all,pdb || true
  k -n "$NS" logs -l app.kubernetes.io/component=controller --prefix --tail=-1 || true
  k get clustermode -o yaml || true
  k -n shop describe deploy web || true
  k get events -A --sort-by=.metadata.creationTimestamp || true
}

cleanup() {
  local status=$?
  if ((created)); then
    if ((status != 0)); then diagnose; fi
    if [[ ${KEEP_CLUSTER:-} == 1 ]]; then
      echo "Kept cluster $CLUSTER; its kubeconfig is $KUBECONFIG"
    else
      "$KIND" delete cluster --name "$CLUSTER"
    fi
  fi
  if [[ ${KEEP_CLUSTER:-} != 1 ]]; then rm -f "$KUBECONFIG"; fi
  exit "$status"
}
trap cleanup EXIT

if "$KIND" get clusters | grep -qx "$CLUSTER"; then
  fail "a kind cluster named $CLUSTER already exists; delete it or set CHART_TEST_CLUSTER"
fi

step "creating kind cluster $CLUSTER"
"$KIND" create cluster --name "$CLUSTER" --wait 2m
created=1
"$KIND" load docker-image "$IMG" --name "$CLUSTER"

# Split IMG into repository and tag. A colon after the last slash starts the
# tag; a colon before it belongs to a registry port.
if [[ ${IMG##*/} == *:* ]]; then
  repo=${IMG%:*} tag=${IMG##*:}
else
  repo=$IMG tag=latest
fi
values=(--set "image.repository=$repo" --set "image.tag=$tag" --set image.pullPolicy=IfNotPresent)
definition=(--set 'definitions[0].name=standard' --set 'definitions[0].modes={normal,peak,standby}'
  --set 'definitions[0].defaultMode=standby')

step "installing the chart"
h install mode-operator dist/chart -n "$NS" --create-namespace "${values[@]}" --wait --timeout 3m
k -n "$NS" rollout status deploy/mode-operator-controller --timeout 2m

step "adding a definition with helm upgrade"
h upgrade mode-operator dist/chart -n "$NS" --reuse-values "${definition[@]}" --wait --timeout 3m
k get clustermodedefinition standard

step "scaling a workload with a ClusterMode"
k create namespace shop
k apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
  labels:
    mode.bitasylum.org/clustermode: default
  annotations:
    mode.bitasylum.org/replicas: "normal=2,*=0"
spec:
  replicas: 0
  selector:
    matchLabels: {app: web}
  template:
    metadata:
      labels: {app: web}
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
---
apiVersion: mode.bitasylum.org/v1alpha1
kind: ClusterMode
metadata:
  name: default
spec:
  definition: standard
  mode: normal
EOF
k -n shop wait deploy/web --for=jsonpath='{.spec.replicas}'=2 --timeout 1m
k -n shop rollout status deploy/web --timeout 2m
k wait clustermode/default --for=jsonpath='{.status.conditions[?(@.type=="Ready")].reason}'=AllSettled \
  --timeout 1m

step "checking that the API server rejects a mode outside the definition"
if out=$(k patch clustermode default --type=merge -p '{"spec":{"mode":"holiday"}}' 2>&1); then
  fail "a mode outside the definition was accepted"
fi
grep -q "must be one of the modes in the ClusterModeDefinition" <<<"$out" ||
  fail "the patch failed for another reason: $out"

step "checking that the transitions admission policy is bound and type-checks against the CRD"
[[ $(k get validatingadmissionpolicybinding mode-operator-transitions -o jsonpath='{.spec.policyName}') == \
  mode-operator-transitions ]] || fail "the binding doesn't name the policy"
# The policy was created with the CRDs, and the API server skips type checking
# while it can't find a CRD's schema, so check unbound copies made now that the
# CRDs are established: the chart's, which must have no warnings, and one with
# a wrong field, which must have one, to show the check runs at all.
# typecheck NAME SED: create an unbound copy of the chart's policy named NAME,
# edited by SED, and print its type-checking warnings.
typecheck() {
  local name=$1 edit=$2
  k get validatingadmissionpolicy mode-operator-transitions -o json |
    jq --arg name "$name" '{apiVersion, kind, metadata: {name: $name}, spec}' |
    sed "$edit" | k create -f - >/dev/null
  k wait "validatingadmissionpolicy/$name" --for=jsonpath='{.status.observedGeneration}'=1 --timeout 1m >/dev/null
  k get validatingadmissionpolicy "$name" -o jsonpath='{.status.typeChecking.expressionWarnings}'
  k delete validatingadmissionpolicy "$name" >/dev/null
}
warnings=$(typecheck typecheck-copy '')
[[ -z $warnings ]] || fail "the admission policy has type-checking warnings: $warnings"
warnings=$(typecheck typecheck-control 's/status\.observedGeneration ==/status.noSuchField ==/')
[[ $warnings == *noSuchField* ]] || fail "type checking didn't catch a wrong field: '$warnings'"

step "uninstalling, which must leave the CRDs and every object in place"
h uninstall mode-operator -n "$NS" --wait
k get crd clustermodes.mode.bitasylum.org clustermodedefinitions.mode.bitasylum.org
k get clustermodedefinition standard
k get clustermode default
[[ $(k -n shop get deploy web -o jsonpath='{.spec.replicas}') == 2 ]] ||
  fail "the workload's replicas changed after uninstall"

step "reinstalling, which must adopt the CRDs and the definition"
h install mode-operator dist/chart -n "$NS" "${values[@]}" "${definition[@]}" --wait --timeout 3m
k -n "$NS" rollout status deploy/mode-operator-controller --timeout 2m
k wait clustermode/default --for=jsonpath='{.status.conditions[?(@.type=="Ready")].reason}'=AllSettled \
  --timeout 1m

step "chart test passed"
