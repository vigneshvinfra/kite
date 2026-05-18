#!/usr/bin/env bash
# generate-cluster-secret.sh
#
# Generates an ArgoCD cluster-registration Secret from the currently
# active kubectl context, prints it to stdout.
#
# Usage:
#   kubectl config use-context prod
#   ./clusters/prod/generate-cluster-secret.sh > /tmp/prod-cluster-secret.yaml
#   kubectl --context docker-desktop apply -f /tmp/prod-cluster-secret.yaml
#
# Note: /tmp/prod-cluster-secret.yaml contains a bearer token — do not commit.
#

set -euo pipefail

# Run while kubectl context is set to the target cluster
TOKEN=$(kubectl -n kube-system get secret argocd-manager-token -o jsonpath='{.data.token}' | base64 -d)
CA_DATA=$(kubectl -n kube-system get secret argocd-manager-token -o jsonpath='{.data.ca\.crt}')
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')

cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: prod-cluster
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
type: Opaque
stringData:
  name: prod
  server: ${SERVER}
  config: |
    {
      "bearerToken": "${TOKEN}",
      "tlsClientConfig": {
        "insecure": false,
        "caData": "${CA_DATA}"
      }
    }
EOF