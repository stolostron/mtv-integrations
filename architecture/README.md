## Provider Manager Controller

The **Provider Manager Controller** (implemented as the `ManagedClusterReconciler`) integrates Advanced Cluster Management (ACM) managed clusters as MTV (Migration Toolkit for Virtualization) providers. Its main responsibilities include:

- **Monitoring ManagedCluster resources:**  
  Activates when a cluster is labeled with `mtv.konveyor.io/provider: "true"` for MTV integration.

- **Creating and managing resources:**
  - **ManagedServiceAccount:**  
    Ensures a service account exists on the managed cluster with token rotation enabled for secure communication.
  - **ClusterPermission:**  
    Grants necessary RBAC permissions (typically `cluster-admin`) to the service account, enabling it to act as an MTV provider.
  - **Provider Secret:**  
    The secret is created by the ManagedServiceAccount controller from the ManagedServiceAccount resource, containing the kubeconfig connectivity token and CA certificate for a managed cluster. For compatibility with the MTV provider, the `ca.crt` value is also duplicated under the `cacert` key in the secret, which is placed in the central MTV namespace (typically `openshift-mtv`).
  - **Provider Resource:**  
    Registers the managed cluster as a Provider custom resource in the MTV namespace, referencing the secret for authentication.

- **Cleanup:**  
  Removes all associated resources and finalizers when a cluster is no longer labeled for MTV.

- **Synchronization:**  
  Ensures the provider resource is only created after the secret is ready, guaranteeing authentication details are in place.

This controller automates the onboarding and offboarding of clusters as MTV providers, ensuring secure and consistent configuration.

## Webhook for MTV Plans

The **MTV plan webhook** is a validating admission webhook for the `Plan` resource (from the Forklift/MTV API). Its purpose is to enforce security and access control when users create or update migration plans:

- **Admission endpoint:**  
  Registered at `/validate-plan` and invoked on `CREATE` and `UPDATE` operations for `forklift.konveyor.io/v1beta1` Plan resources.

- **User impersonation:**  
  Impersonates the requesting user to check their permissions.

- **Target namespace access check:**
  - Extracts the target provider & namespace and destination provider (cluster) & namespace from the Plan spec.
  - Uses a dynamic client with impersonation to attempt access to the `kubevirtprojects` resource in the target namespace on the destination cluster.
  - If the user lacks access to an of the four target and destination fields, the webhook denies the request with a clear error message.

- **Security enforcement:**  
  Ensures only users with appropriate permissions can create migration plans targeting specific namespaces, preventing privilege escalation or unauthorized migrations.

The webhook is deployed with the controller and uses certificates for secure communication. It is critical for multi-tenant and secure environments, as it enforces RBAC at the time of plan creation.

---

**Summary:**

- The **Provider Manager Controller** automates the registration and lifecycle of managed clusters as MTV providers, handling service accounts, permissions, and provider resources.
- The **MTV plan webhook** enforces access control for migration plans, ensuring users can only source and target, clusters and namespaces they are authorized to access by impersonating the user and checking permissions at admission time.