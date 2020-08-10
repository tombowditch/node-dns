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
