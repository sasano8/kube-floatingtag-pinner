#!/usr/bin/env bash
# End-to-end test against a real cluster: kind (a real kubelet and containerd) plus a local registry,
# so that "the remote's tag moved" can be made to happen at will, with no Docker Hub, no rate limit.
#
# Needs: docker, kind, kubectl, crane. Usage: test/e2e/run.sh /path/to/pinner
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

# run_in <namespace> <args...>: runs pinner on one namespace, keeps its output in $LOG, returns its exit code
run_in() {
  local ns=$1; shift
  set +e
  "$PINNER" "$@" --kubeconfig "$KUBECONFIG" --namespace "$ns" >"$LOG" 2>&1
  local rc=$?
  set -e
  cat "$LOG"
  return $rc
}
run_pinner() { run_in "$NS" "$@"; }
expect() { grep -Eq "$1" "$LOG" || fail "expected a line matching: $1"; }
expect_not() { ! grep -Eq "$1" "$LOG" || fail "unexpected line matching: $1"; }

# wait_local_matches: the kubelet refreshes Node.status.images only every few minutes, so what pull and trigger
# read as "the local state" can be behind what was just pulled. Wait until pull sees the tag as matching.
wait_local_matches() {
  local ns=${1:-$NS}
  local deadline=$((SECONDS + 420))
  while :; do
    run_in "$ns" pull --dry-run >/dev/null 2>&1 || true
    grep -q 'msg="pull match"' "$LOG" && return 0
    [ "$SECONDS" -lt "$deadline" ] || fail "Node.status.images did not show the pulled image within 7 minutes"
    sleep 10
  done
}

# push_to <image> <label>: builds a tiny image whose content depends on <label>, and pushes it
push_to() {
  local dir="${WORK}/img-$(echo "$1" | tr -c 'a-zA-Z0-9\n' _)-$2"
  mkdir -p "$dir"
  printf 'FROM busybox:1.36\nLABEL e2e.version=%s\nCMD ["sleep","3600"]\n' "$2" >"$dir/Dockerfile"
  docker build -q -t "$1" "$dir" >/dev/null
  docker push -q "$1" >/dev/null
}
push_image() { push_to "$IMAGE" "$1"; }

# new_app <namespace> <image> <replicas>: a Deployment "app" that pinner tracks
new_app() {
  kubectl create namespace "$1"
  kubectl -n "$1" create deployment app --image="$2" --replicas="$3"
  kubectl -n "$1" annotate deployment app floatingtag-pinner/enabled=true
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

step "collect: the annotated Deployment and its tracked image are listed"
run_pinner collect || fail "collect exited non-zero"
expect "e2e/Deployment/app +localhost:5001/e2e/app:latest +yes"

step "wait until the nodes report the image the Pod pulled (Node.status.images is refreshed late)"
wait_local_matches

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
expect 'msg="pull match".*via=pulling'   # from what the pull Pods reported, not from Node.status.images

step "wait until the nodes report the pulled image"
wait_local_matches

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
template_annotations=$(kubectl -n "$NS" get deployment app -o jsonpath='{.spec.template.metadata.annotations}')
case "$template_annotations" in *floatingtag-pinner/restartedAt*) ;; *)
  fail "the Pod template must carry the rollout annotation (got: ${template_annotations:-none})" ;;
esac
image_now=$(kubectl -n "$NS" get deployment app -o jsonpath='{.spec.template.spec.containers[0].image}')
[ "$image_now" = "$IMAGE" ] || fail "the image field must not be touched (got: $image_now)"
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
wait_local_matches

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

step "error: the registry has no such image -> pull exits non-zero, trigger says not-pulled"
new_app e2e-missing "localhost:${REG_PORT}/e2e/missing:latest" 0
if run_in e2e-missing pull; then fail "pull must exit non-zero when the registry cannot answer"; fi
expect 'msg="pull error"'
run_in e2e-missing trigger || fail "trigger exited non-zero"
expect 'msg="trigger not-pulled"'

step "not-pulled and not-running: an image no node holds, with nothing running"
FRESH="localhost:${REG_PORT}/e2e/fresh:latest"
push_to "$FRESH" v1
new_app e2e-fresh "$FRESH" 0
run_in e2e-fresh pull --on-new=ignore || fail "pull exited non-zero"
expect 'msg="pull not-pulled".*outcome=ignored'
run_in e2e-fresh pull --dry-run || fail "pull --dry-run exited non-zero"
expect 'msg="pull not-pulled".*outcome=would-pull'
run_in e2e-fresh pull || fail "pull exited non-zero"
expect 'msg="pull not-pulled".*outcome=pulled'
expect 'msg="pull match".*via=pulling'
wait_local_matches e2e-fresh
run_in e2e-fresh trigger || fail "trigger exited non-zero"
expect 'msg="trigger not-running"' # pulled, but scaled to zero: nothing to compare
kubectl -n default get pods -l app.kubernetes.io/managed-by=floatingtag-pinner -o name | grep -q . &&
  fail "the pull Pods must be deleted after the pull"

step "a Pod that restarts by itself follows the node's tag; no action is needed"
CRASH="localhost:${REG_PORT}/e2e/crash:latest"
push_to "$CRASH" v1
new_app e2e-crash "$CRASH" 1
kubectl -n e2e-crash patch deployment app --type=json -p '[
  {"op":"add","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"},
  {"op":"add","path":"/spec/template/spec/containers/0/command","value":["sh","-c","sleep 20; exit 1"]}]'
for _ in $(seq 60); do # wait for the first container to have pulled and started
  [ -n "$(kubectl -n e2e-crash get pods -o jsonpath='{.items[0].status.containerStatuses[0].imageID}' 2>/dev/null)" ] && break
  sleep 2
done
crash_pod=$(kubectl -n e2e-crash get pods -o jsonpath='{.items[0].metadata.name}')
push_to "$CRASH" v2
run_in e2e-crash pull || fail "pull exited non-zero"
expect 'msg="pull match".*via=pulling'
wait_local_matches e2e-crash
deadline=$((SECONDS + 180))
until run_in e2e-crash trigger >/dev/null 2>&1 && grep -q 'msg="trigger match"' "$LOG"; do
  [ "$SECONDS" -lt "$deadline" ] || fail "the restarted container never came to run the node's digest"
  sleep 10
done
expect_not 'action requested'
[ "$(kubectl -n e2e-crash get pods -o jsonpath='{.items[0].metadata.name}')" = "$crash_pod" ] ||
  fail "the Pod must be the same one: only its container restarted"
[ "$(kubectl -n e2e-crash get pods -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}')" -gt 0 ] ||
  fail "the container must have restarted"

step "a multi-arch image: the tag's digest is the index digest, and the node reports the same one"
MULTI="localhost:${REG_PORT}/e2e/multi:latest"
crane copy busybox:1.36 "$MULTI" # an image index (several platforms), copied as it is
new_app e2e-multi "$MULTI" 1
kubectl -n e2e-multi patch deployment app --type=json -p '[
  {"op":"add","path":"/spec/template/spec/containers/0/command","value":["sleep","3600"]}]'
kubectl -n e2e-multi rollout status deployment/app --timeout=180s
wait_local_matches e2e-multi
run_in e2e-multi trigger || fail "trigger exited non-zero"
expect 'msg="trigger match"'
expect_not 'msg="trigger mismatch"'

step "private registry: pull works when the nodes hold the credentials (kubelet reads config.json)"
AUTH_PORT=5002
AUTH_NAME=kind-registry-auth
PRIVATE="localhost:${AUTH_PORT}/e2e/private:latest"
mkdir -p "${WORK}/auth" "${WORK}/docker"
docker run --rm --entrypoint htpasswd httpd:2 -Bbn e2e secret >"${WORK}/auth/htpasswd"
docker run -d --restart=always -p "127.0.0.1:${AUTH_PORT}:5000" --network bridge --name "$AUTH_NAME" \
  -v "${WORK}/auth:/auth" -e REGISTRY_AUTH=htpasswd -e REGISTRY_AUTH_HTPASSWD_REALM=e2e \
  -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd registry:2 >/dev/null
trap 'docker rm -f "$AUTH_NAME" >/dev/null 2>&1 || true; cleanup' EXIT
docker network connect kind "$AUTH_NAME"
for node in $(kind get nodes --name "$CLUSTER"); do
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/localhost:${AUTH_PORT}"
  printf '[host."http://%s:5000"]\n' "$AUTH_NAME" |
    docker exec -i "$node" cp /dev/stdin "/etc/containerd/certs.d/localhost:${AUTH_PORT}/hosts.toml"
done
CRED_DIR="${WORK}/docker" # a config.json of its own: pinner gets it only through --credential
DOCKER_CONFIG="$CRED_DIR" docker login "localhost:${AUTH_PORT}" -u e2e -p secret >/dev/null
DOCKER_CONFIG="$CRED_DIR" push_to "$PRIVATE" v1
new_app e2e-private "$PRIVATE" 0

# no credentials for pinner: the digest cannot be looked up
if run_in e2e-private pull --pull-namespace e2e-private; then
  fail "pull must fail while pinner has no credentials"
fi
expect 'msg="pull error"'
expect_not 'outcome=pull-failed' # it never got as far as pulling

# pinner has them (--credential), but the nodes have no credentials to pull with
if run_in e2e-private pull --pull-namespace e2e-private --pull-timeout 90s --credential "${CRED_DIR}/config.json"; then
  fail "pull must fail while the nodes have no credentials"
fi
expect 'msg="pull error".*state=not-pulled.*outcome=pull-failed'

# the credentials go on the nodes, where kubelet reads them (nothing is added to the Pod or its namespace)
for node in $(kind get nodes --name "$CLUSTER"); do
  docker cp "${CRED_DIR}/config.json" "${node}:/var/lib/kubelet/config.json"
  docker exec "$node" systemctl restart kubelet
done
kubectl wait --for=condition=Ready node --all --timeout=120s
run_in e2e-private pull --pull-namespace e2e-private --credential "${CRED_DIR}/config.json" ||
  fail "pull exited non-zero with credentials on pinner and on the nodes"
expect 'msg="pull not-pulled".*outcome=pulled'
expect 'msg="pull match".*via=pulling'
kubectl -n e2e-private get pods -l app.kubernetes.io/managed-by=floatingtag-pinner -o name | grep -q . &&
  fail "the pull Pods must be deleted after the pull"

step "log options"
run_pinner pull --log-level=warn || fail "pull exited non-zero"
expect_not 'msg="pull match"' # INFO is hidden
run_pinner pull --log-states=mismatch,error || fail "pull exited non-zero"
expect_not 'msg="pull match"' # only the listed states are shown
run_pinner pull --log-states=match || fail "pull exited non-zero"
expect 'msg="pull match"'

echo; echo "e2e passed"
