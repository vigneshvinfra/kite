# Debugging: Ingress returns 502/504 while pods appear healthy
502/504 is a claim made by the ingress controller about its experience reaching the pod.

502 means that the Ingress controller successfully received the request but couldn't reach backend pods. It could also mean that the Ingress controller could not connect to Service endpoints or received errors when it tried or got bad response. 502 confirms that the route exists but the upstream service is unavailable or unhealthy.


504 Gateway Timeout in Kubernetes means the Ingress Controller connected to the backend but never got a response in time from a backend service. The Ingress's read timeout might be lower than the app's actual response time. This could also happen due to the application being slow.

  ## Table of contents

 - [What "healthy pod" actually means](#what-healthy-pod-actually-means)
  - [Request flow](#request-flow)
  - [Troubleshooting](#troubleshooting)
    - [Hop 1: client -> LB](#hop-1-client---lb)
    - [Hop 2: LB -> Ingress Controller](#hop-2-lb---ingress-controller)
    - [Hop 3: Ingress Controller](#hop-3-ingress-controller)
    - [Hop 4: Ingress Controller -> Service](#hop-4-ingress-controller---service)
    - [Hop 5: Service -> Endpoints](#hop-5-service---endpoints)
    - [Hop 6: Endpoints -> Pod](#hop-6-endpoints---pod)
  - [Additional checks](#additional-checks)
    - [NetworkPolicy blocking ingress-nginx to pod traffic](#networkpolicy-blocking-ingress-nginx-to-pod-traffic)
  
## What "healthy pod" actually means
STATUS: Running doesn't really mean the pod started without errors. They only tell us:

  - The pod is currently running on a node
  - The pod started without errors.
  - The configured liveness and readiness probes are passing.

They do **not** tell us:

- The pod is reachable from outside.
- The Service selector matches the pod's labels.
- An ingress-nginx rule actually routes traffic to this Service.
- The LB in front of ingress-nginx is reachable from the client.


A clean 502/504 with healthy pods means **a layer above the pod is
broken**. 

## Request Flow
```yaml
client → LB → Ingress Controller → Service → Endpoints → Pod
```
Each component is a hop that can fail independently. We need to work top-down to find where exactly the issue is.

## Troubleshooting

### Hop 1: client -> LB
We need to first see if the DNS resolves to a public IP and we get a response back.

```bash
dig +short <elb-hostname>
curl -v --max-time 5 https://<elb-hostname>/healthz
```

If the DNS resolves to a private ip and the client is outside the VPC. The requests can fail as the Load balancer may have internal-scheme. We would need to change the load balancer scheme to be internet-facing.

In case if curl hangs and times out eventually. we would probably need to check the routing or security group or NACLs between client and Load balancer is blocking the traffic. We also need to check if the VPC has internet gateway and a route to the internet.

### Hop 2: LB -> Ingress Controller
We need to see if the targets for the Load balancer are `healthy`. For an NLB pointing at ingress-nginx pods with target-type as ip, we can check the below command

```bash
aws elbv2 describe-target-health --target-group-arn <ingress-nginx-controller-tg-arn>
```
If any targets are in an unhealthy state, we need to find the
reason and follow the steps to triage:

- Check if the Target Group is routing to the correct backend port.
- Check if the health check path is correct.
- Check if the backend has any port listening.
- Check the Security Group attached to the node to see if it allows traffic from the LB.


### Hop 3: Ingress Controller
Check if the nginx ingress controller pod itself is healthy
```bash
kubectl -n ingress-nginx get pods
kubectl -n ingress-nginx logs deploy/ingress-nginx-controller --tail=50
```

- Check if any Nginx ingress pod is in `CrashLoopBackOff`
- Check for reload errors (`failed to reload nginx`). This could happen due to a bad Ingress in the cluster to break the config. Run `kubectl get ingress -A` to read the events.
- 502 entries in the access log with `upstream connect failed`. This means that the ingress nginx controller reached the Service but couldn't connect to the pod.
- 504 entries with `upstream timed out` → pod is slow or hanging.

### Hop 4: Ingress Controller -> Service
Check if the rules have the right host, path and a service that actually exists. We also need to check the ingressClassName to match the controller. 
```bash
kubectl -n <ns> get ingress <name> -o yaml
kubectl -n <ns> describe ingress <name>
```

- `spec.rules[].host` must match the `Host` header the client sends. Browsers usually send the URL hostname; if the Ingress says `myapp.example.com` and if we are curling the raw ELB DNS, we would only get the ingress-nginx default backend (404 or empty). We should always send the right `Host` header (`curl -H "Host: myapp.example.com"`) or use a
  host-less Ingress for testing.
- `spec.rules[].http.paths[].backend.service.name` and `port` must
  match an existing Service. A typo in the service name means 503.
- `ingressClassName` must match the controller's `IngressClass` (in our case this is `nginx`). Wrong class means the Ingress is ignored entirely.

### Hop 5: Service -> Endpoints
Kubernetes Service uses Endpoint objects which holds the actual list of pod IPs to route the request.
  ```bash
  kubectl get endpoints <service-name>
  ```

-  Check if the Endpoints list is **empty**. This could happen when the Service `selector` doesn't match any pod labels.

    ```bash
    kubectl get service <name> -oyaml | grep selector
    kubectl get pod <pod> -oyaml | grep label
    ```
- If Endpoints list **has IPs** but the Service port doesn't match container port, This could result in 502 (`upstream connect failed`). We would need to fix the Service's `targetPort` or the container's `containerPort`.
- Endpoints list has IPs of pods that exist but are not ready. This could also result in a 502 for the brief window before the endpoint controller removes them.

- Backend Protocol Mismatch - If the pod serves https but the ingress treats it as HTTP or vice versa - we would get a 502

### Hop 6: Endpoints -> Pod

1. **Check that the pod is actually accepting traffic.**

```bash
   kubectl debug -it <pod> --image=nicolaka/netshoot --target=<container> -- ss -tlnp
```

   - Listening on `0.0.0.0:8000` (or `:::8000`) — the application is reachable from outside the pod. Good.
   - Listening on `127.0.0.1:8000` — the application is only reachable from *inside* the pod. The **Service can never hit it**. Fix the app to bind to `0.0.0.0`.

2. **Check that other pods can reach it directly via the pod IP.**

```bash
   POD_IP=$(kubectl -n <ns> get pod <pod> -o jsonpath='{.status.podIP}')

   kubectl run curl --rm -it --image=curlimages/curl --restart=Never -- \
     curl -v http://$POD_IP:<port>
```

3. **Check that the application is reachable over the Service DNS.**

```bash
   kubectl run curl --rm -it --image=curlimages/curl --restart=Never -- \
     curl -v http://<service>.<ns>.svc.cluster.local:<svc-port>/
```         

## Additional Checks

### NetworkPolicy blocking ingress-nginx to pod traffic

NetworkPolicy is **default-allow** for namespaces that have none, and
**default-deny** for namespaces that have at least one. Adding the first
NetworkPolicy to a namespace that previously had none typically breaks
everything immediately.

```
kubectl get networkpolicy
kubectl describe networkpolicy <name>
```

Look at the `Ingress` rules. Make sure either:

- A rule explicitly allows traffic from the `ingress-nginx` namespace
  (`namespaceSelector` + optionally `podSelector`), or
- The NetworkPolicy doesn't apply to this pod (selector mismatch).

In this repo, see `helm/myapp/templates/networkpolicy.yaml:1-30`
for the pattern: allow from `ingress-nginx` namespace, allow from
Prometheus, allow DNS egress to kube-system. Anything else is denied.
