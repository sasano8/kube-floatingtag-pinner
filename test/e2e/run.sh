#!/usr/bin/env bash
# End-to-end test against a real cluster: kind (a real kubelet and containerd) plus a local registry,
# so that "the remote's tag moved" can be made to happen at will, with no Docker Hub, no rate limit.
#
# Needs: docker, kind, kubectl. Usage: test/e2e/run.sh /path/to/pinner
# Not run by CI by default (see .github/workflows/e2e.yml).
set -euo pipefail

PINNER=${1:?usage: run.sh /path/to/pinner}
CLUSTER=floatingtag-e2e
REG_NAME=kind-registry
REG_PORT=5001
IMAGE="localhost:${REG_PORT}/e2e/app:latest"
NS=e2e
WORK=$(mktemp -d)
LOG="${WORK}/pinner.log"

cleanup() {
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  docker rm -f "$REG_NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

step() { echo; echo "== $*"; }
fail() { echo "FAIL: $*" >&2; echo "--- last pinner output:" >&2; cat "$LOG" >&2 || true; exit 1; }

# run_pinner <args...>: runs pinner, keeps its output in $LOG, returns its exit code
run_pinner() {
  set +e
  "$PINNER" "$@" --kubeconfig "$KUBECONFIG" --namespace "$NS" >"$LOG" 2>&1
  local rc=$?
  set -e
  cat "$LOG"
  return $rc
}
expect() { grep -Eq "$1" "$LOG" || fail "expected a line matching: $1"; }
expect_not() { ! grep -Eq "$1" "$LOG" || fail "unexpected line matching: $1"; }

# push_image <label>: builds a tiny image whose content depends on <label>, and pushes it as :latest
push_image() {
  local dir="${WORK}/img-$1"
  mkdir -p "$dir"
  printf 'FROM busybox:1.36\nLABEL e2e.version=%s\nCMD ["sleep","3600"]\n' "$1" >"$dir/Dockerfile"
  docker build -q -t "$IMAGE" "$dir" >/dev/null
  docker push -q "$IMAGE" >/dev/null
}

step "registry and cluster"
if [ "$(docker inspect -f '{{.State.Running}}' "$REG_NAME" 2>/dev/null || true)" != true ]; then
  docker run -d --restart=always -p "127.0.0.1:${REG_PORT}:5000" --network bridge --name "$REG_NAME" registry:2 >/dev/null
fi
cat <<YAML | kind create cluster --name "$CLUSTER" --wait 120s --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
YAML
for node in $(kind get nodes --name "$CLUSTER"); do
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/localhost:${REG_PORT}"
  printf '[host."http://%s:5000"]\n' "$REG_NAME" |
    docker exec -i "$node" cp /dev/stdin "/etc/containerd/certs.d/localhost:${REG_PORT}/hosts.toml"
done
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "$REG_NAME")" = null ]; then
  docker network connect kind "$REG_NAME"
fi
export KUBECONFIG="${WORK}/kubeconfig"
kind get kubeconfig --name "$CLUSTER" >"$KUBECONFIG"

step "deploy v1 (annotated)"
push_image v1
kubectl create namespace "$NS"
kubectl -n "$NS" create deployment app --image="$IMAGE"
kubectl -n "$NS" patch deployment app --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Always"}]'
kubectl -n "$NS" annotate deployment app floatingtag-pinner/enabled=true
kubectl -n "$NS" rollout status deployment/app --timeout=180s

step "pull: the local tag already follows the remote -> match"
run_pinner pull || fail "pull exited non-zero"
expect 'msg="pull match"'
expect_not 'msg="pull mismatch"'

step "the remote's tag moves (v2)"
push_image v2

step "pull --dry-run: reports what would be pulled, pulls nothing"
run_pinner pull --dry-run || fail "pull --dry-run exited non-zero"
expect 'msg="pull mismatch".*outcome=would-pull'
run_pinner pull --dry-run || fail "second dry run exited non-zero"
expect 'msg="pull mismatch".*outcome=would-pull' # still mismatch: nothing was pulled

step "pull: mismatch -> match by pulling"
run_pinner pull || fail "pull exited non-zero"
expect 'msg="pull mismatch".*outcome=pulled'
expect 'msg="pull match".*via=pulling'

step "pull again: nothing to do"
run_pinner pull || fail "pull exited non-zero"
expect 'msg="pull match"'
expect_not 'msg="pull mismatch"'

step "trigger: the running Pod still runs v1, the local tag is v2 -> mismatch (the default action changes nothing)"
run_pinner trigger || fail "trigger exited non-zero"
expect 'msg="trigger mismatch"'
kubectl -n "$NS" get pods -o name | grep -q . || fail "trigger must not delete Pods"

step "trigger --dry-run with on-mismatch=rollout: says what it would do, changes nothing"
kubectl -n "$NS" annotate deployment app floatingtag-pinner/on-mismatch=rollout
gen_before=$(kubectl -n "$NS" get deployment app -o jsonpath='{.metadata.generation}')
run_pinner trigger --dry-run || fail "trigger --dry-run exited non-zero"
expect 'dry-run: would run action.*action=rollout'
[ "$(kubectl -n "$NS" get deployment app -o jsonpath='{.metadata.generation}')" = "$gen_before" ] ||
  fail "a dry run must not change the Deployment"

step "trigger with on-mismatch=rollout: a new rollout starts (only an annotation on the Pod template)"
run_pinner trigger || fail "trigger exited non-zero"
expect 'msg="trigger mismatch"'
expect 'trigger action requested.*action=rollout'
kubectl -n "$NS" get deployment app -o jsonpath='{.spec.template.metadata.annotations}' | grep -q 'floatingtag-pinner/restartedAt' ||
  fail "the Pod template must carry the rollout annotation"
[ "$(kubectl -n "$NS" get deployment app -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$IMAGE" ] ||
  fail "the image field must not be touched"
kubectl -n "$NS" rollout status deployment/app --timeout=180s

step "trigger again: the new Pod runs the local digest -> match, and nothing more is requested"
run_pinner trigger || fail "trigger exited non-zero"
expect 'msg="trigger match"'
expect_not 'msg="trigger mismatch"'
expect_not 'action requested'

step "the remote's tag moves again (v3); pull it"
push_image v3
run_pinner pull || fail "pull exited non-zero"
expect 'msg="pull match".*via=pulling'

step "trigger --dry-run with on-mismatch=delete-pods: says what it would do, deletes nothing"
kubectl -n "$NS" annotate --overwrite deployment app floatingtag-pinner/on-mismatch=delete-pods
old_pod=$(kubectl -n "$NS" get pods -o jsonpath='{.items[0].metadata.name}')
run_pinner trigger --dry-run || fail "trigger --dry-run exited non-zero"
expect 'dry-run: would run action.*action=delete-pods'
kubectl -n "$NS" get pod "$old_pod" >/dev/null || fail "a dry run must not delete the Pod"

step "trigger with on-mismatch=delete-pods: the differing Pod is deleted, the Deployment is untouched"
gen_before=$(kubectl -n "$NS" get deployment app -o jsonpath='{.metadata.generation}')
run_pinner trigger || fail "trigger exited non-zero"
expect 'trigger action requested.*action=delete-pods'
kubectl -n "$NS" wait --for=delete "pod/${old_pod}" --timeout=120s
kubectl -n "$NS" rollout status deployment/app --timeout=180s
[ "$(kubectl -n "$NS" get deployment app -o jsonpath='{.metadata.generation}')" = "$gen_before" ] ||
  fail "delete-pods must not change the Deployment"

step "trigger again: the new Pod runs the local digest -> match"
run_pinner trigger || fail "trigger exited non-zero"
expect 'msg="trigger match"'
expect_not 'msg="trigger mismatch"'

step "log options"
run_pinner pull --log-level=warn || fail "pull exited non-zero"
expect_not 'msg="pull match"' # INFO is hidden
run_pinner pull --log-states=mismatch,error || fail "pull exited non-zero"
expect_not 'msg="pull match"' # only the listed states are shown
run_pinner pull --log-states=match || fail "pull exited non-zero"
expect 'msg="pull match"'

echo; echo "e2e passed"
