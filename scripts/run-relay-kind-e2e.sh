#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CLUSTER_NAME="${CLUSTER_NAME:-codewire-relay-e2e}"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
RELAY_NAMESPACE="${RELAY_NAMESPACE:-relay-test}"
RELAY_RELEASE="${RELAY_RELEASE:-cw-relay-test}"
RELAY_NAME="${RELAY_NAME:-relay-test}"
RELAY_PORT="${RELAY_PORT:-18080}"
RELAY_URL="http://127.0.0.1:${RELAY_PORT}"
RELAY_TOKEN="${RELAY_TOKEN:-dev-secret}"
RELAY_IMAGE_REPO="${RELAY_IMAGE_REPO:-codewire-relay}"
RELAY_IMAGE_TAG="${RELAY_IMAGE_TAG:-kind-e2e}"
RELAY_IMAGE="${RELAY_IMAGE_REPO}:${RELAY_IMAGE_TAG}"

cleanup() {
  if [[ -n "${PORT_FORWARD_PID:-}" ]]; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
    wait "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  kind create cluster --name "${CLUSTER_NAME}"
fi

docker build -t "${RELAY_IMAGE}" "${ROOT_DIR}"
kind load docker-image "${RELAY_IMAGE}" --name "${CLUSTER_NAME}"

helm upgrade --install "${RELAY_RELEASE}" "${ROOT_DIR}/charts/codewire-relay" \
  --kube-context "${KUBE_CONTEXT}" \
  --namespace "${RELAY_NAMESPACE}" \
  --create-namespace \
  --set fullnameOverride="${RELAY_NAME}" \
  --set image.repository="${RELAY_IMAGE_REPO}" \
  --set image.tag="${RELAY_IMAGE_TAG}" \
  --set image.pullPolicy=IfNotPresent \
  --set relay.baseURL="${RELAY_URL}" \
  --set relay.authMode=token \
  --set relay.authToken="${RELAY_TOKEN}" \
  --set persistence.enabled=false \
  --set ssh.service.type=ClusterIP

kubectl --context "${KUBE_CONTEXT}" -n "${RELAY_NAMESPACE}" rollout status deployment/"${RELAY_NAME}" --timeout=120s

kubectl --context "${KUBE_CONTEXT}" -n "${RELAY_NAMESPACE}" port-forward svc/"${RELAY_NAME}"-http "${RELAY_PORT}":8080 >/tmp/codewire-relay-kind-port-forward.log 2>&1 &
PORT_FORWARD_PID=$!

for _ in $(seq 1 30); do
  if curl -sf "${RELAY_URL}/healthz" >/dev/null; then
    break
  fi
  sleep 1
done

if ! curl -sf "${RELAY_URL}/healthz" >/dev/null; then
  echo "relay health check failed; port-forward log:" >&2
  cat /tmp/codewire-relay-kind-port-forward.log >&2
  exit 1
fi

cd "${ROOT_DIR}"
CODEWIRE_RELAY_TEST_URL="${RELAY_URL}" \
CODEWIRE_RELAY_TEST_TOKEN="${RELAY_TOKEN}" \
go test ./tests -tags='integration kind' -run TestRelayNetworkMessagingThreeSessionsKind -count=1 -v
