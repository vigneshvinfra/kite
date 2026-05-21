#!/usr/bin/env bash
# Regenerates the Argo CD cluster-registration Secret for the dev k3s cluster.
#
# Where to run:
#   On the dev EC2 itself (SSM in first). Cloud-init created the
#   `argocd-manager` ServiceAccount + token Secret on first boot; this script
#   just re-reads those and renders the YAML Argo CD expects.
#
# Usage:
#   sudo ./argo-dev-cluster.sh > /tmp/dev-cluster-secret.yaml
#
#   # then on a laptop pointed at the prod EKS context:
#   kubectl apply -f /tmp/dev-cluster-secret.yaml
#
# Optional override:
#   SERVER=https://10.0.1.42:6443 ./argo-dev-cluster.sh
#     # If unset, the EC2's private IP is read from the IMDSv2 endpoint.
#
# Note: output contains a cluster-admin bearer token — do not commit.

set -euo pipefail

# ---- SA token + cluster CA (populated by k8s token controller) --------------
TOKEN=$(kubectl -n kube-system get secret argocd-manager-token \
          -o jsonpath='{.data.token}' | base64 -d)
CA_DATA=$(kubectl -n kube-system get secret argocd-manager-token \
            -o jsonpath='{.data.ca\.crt}')

# ---- Server URL — prefer explicit override, else discover via IMDSv2 --------
if [[ -z "${SERVER:-}" ]]; then
  IMDS_TOKEN=$(curl -fsS -X PUT \
    "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
  PRIVATE_IP=$(curl -fsS \
    -H "X-aws-ec2-metadata-token: ${IMDS_TOKEN}" \
    http://169.254.169.254/latest/meta-data/local-ipv4)
  SERVER="https://${PRIVATE_IP}:6443"
fi

# ---- Render the Argo CD cluster Secret to stdout ----------------------------
cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: dev-cluster
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
    env: dev
stringData:
  name: dev
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
