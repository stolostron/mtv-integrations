# Custom CA for MTV provider secrets

The ManagedCluster controller copies the ManagedServiceAccount (MSA) token secret into the MTV provider secret that Forklift uses to connect to a managed cluster.

| Secret | Namespace | Name | CA key |
|---|---|---|---|
| MSA token secret (source) | managed cluster name (on the hub) | `<cluster>-mtv` | `ca.crt` |
| Provider secret (Forklift) | `mtv-integrations` | `<cluster>-mtv` | `cacert` |

Sync behavior:

- `token` is always kept in sync with the MSA token (including rotation).
- `cacert` stays in sync with the MSA `ca.crt`, and any extra PEM certificates already present in `cacert` are preserved if they are usable for TLS. The controller uses the same rules as `x509.CertPool.AppendCertsFromPEM` (what Forklift uses): each block must be a PEM `CERTIFICATE` with no headers whose DER parses as an x509 certificate. The controller appends the current MSA `ca.crt` only if that certificate is missing from the bundle. Trailing junk, invalid PEM, and PEM that is not x509 are stripped. If no TLS-usable certificate remains, `cacert` is overwritten with the MSA `ca.crt`.

## When to use this

If the managed cluster API server is signed by a **custom CA** that is not in the MSA `ca.crt` (often `kube-root-ca.crt`), the provider connection fails until that CA is in the provider secret `cacert` field.

Append the custom CA to `cacert`. Do not replace the whole field with a non-PEM value. Opaque non-PEM `cacert` is treated as drift and overwritten with the MSA `ca.crt`. Junk mixed into an otherwise valid bundle (for example a trailing `3`, or `BEGIN CERTIFICATE` / `test==` / `END CERTIFICATE`) is stripped on reconcile; valid custom CAs are kept.

## Add a custom CA

Edit the **provider** secret (`mtv-integrations/<cluster>-mtv`), not only the MSA secret in the cluster namespace.

```bash
CLUSTER=<managed-cluster-name>
CUSTOM_CA=/path/to/custom-ca.pem

kubectl -n mtv-integrations get secret "${CLUSTER}-mtv" \
  -o jsonpath='{.data.cacert}' | base64 -d > /tmp/cacert.pem
cat "${CUSTOM_CA}" >> /tmp/cacert.pem
kubectl -n mtv-integrations patch secret "${CLUSTER}-mtv" --type=merge \
  -p "{\"data\":{\"cacert\":\"$(base64 < /tmp/cacert.pem | tr -d '\n')\"}}"
```

On later reconciles the controller will not revert extra TLS-usable PEM certificates in `cacert`.
