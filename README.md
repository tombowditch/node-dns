# node-dns

Quick and easy way to set Cloudflare external DNS for hostPort pods on Kubernetes.

# Example

Take this setup:

- 3 nodes (external IPs of `2.2.2.1`, `2.2.2.2`, `2.2.2.3`)
- `whoami` pod (1 replica) deployed on `2.2.2.2` with a `hostPort`
- Your DNS (`whoami.example.com`) needs to point to your application (no matter what node(s) it is on)

We cannot just point our DNS to all 3 node IPs as kube-proxy does not forward traffic for host networking.

The solution:
`node-dns` is able to solve this by a simple annotation:

```yaml
annotations:
  tombowdit.ch/node-dns: "whoami.example.com"
```

Every 30 seconds, `node-dns` will check your local pod and the nodes it is deployed on, check the records currently at Cloudflare and ensure the match up.

# Why would I need this?

- Your load balancer (example: scaleway) doesn't support UDP and you need to listen on a port that isn't achievable with NodePort
- You don't want to pay for a load balancer

# Environment Variables

- `CF_API_TOKEN` - set to your Cloudflare API token
- `TYPE` - set to `outside` if you wish to use your \$HOME/.kube/config rather than cluster RBAC (else you can leave it)
- `NAMESPACE` - the namespace to look for pods in (default: `metrics`)

# Kubernetes: deployment example

```yaml
kind: Deployment
apiVersion: apps/v1
metadata:
  namespace: metrics
  name: node-dns
  labels:
    app: node-dns

spec:
  replicas: 1
  selector:
    matchLabels:
      app: node-dns
  template:
    metadata:
      labels:
        app: node-dns
    spec:
      serviceAccountName: node-dns
      containers:
        - name: node-dns
          image: docker.pkg.github.com/tombowditch/node-dns/node-dns:latest
          imagePullPolicy: Always
          env:
            - name: CF_API_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cloudflare-api-token-secret
                  key: api-token
            - name: NAMESPACE
              value: "metrics"
          args: []
```

# Kubernetes: RBAC example

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: node-dns
  namespace: metrics
---
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRole
metadata:
  name: node-dns
  namespace: metrics
rules:
  - apiGroups: [""]
    resources:
      - nodes
      - pods
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRoleBinding
metadata:
  name: node-dns
subjects:
  - kind: ServiceAccount
    name: node-dns
    namespace: metrics
roleRef:
  kind: ClusterRole
  name: node-dns
  apiGroup: rbac.authorization.k8s.io
```
