# mtv-integrations

## Summary

The `mtv-controller-manager` automates the integration between ACM (Advanced Cluster Management) and the Migration Toolkit for Virtualization (MTV) with live migration. It monitors `ManagedCluster` resources and, when clusters are labeled as `acm/cnv-operator-install=true`, it provisions and manages the necessary Kubernetes resources to enable migration workflows between clusters.

## Kubernetes Resources Managed

- **ManagedCluster**: Representatino of the clusters that will be used as the source and target for a live migraiton
- **ManagedServiceAccount**: Creates a service account on a managed cluster and retrieves an access token for 
authentication.
- **ClusterPermission**: Grants required permissions (such as `cluster-admin`) to the service account.
- **Provider**: Defines Migration Toolkit for Virtualization provider resources (used as source and target clusters
in live migrations)
- **Secret**: Formats and manages secrets, created by the ManagedServiceAccount resource,  containing the cluster API 
URL and CA certificates for use by the Migration Toolkit for Virtualization provider resources.

## Build, Run, Deploy, and Undeploy

### Build locally `./bin/manager`

```sh
make build
```

### Run Locally

```sh
make run
```

### Deploy to Cluster

```sh
make deploy
```

### Undeploy from Cluster

```sh
make undeploy
```

### Build container image

* Set the environemnt variable `REGISTRY_BASE=quay.io/amd`, to a value that represents you destination repository.
* Make sure you are authenticated with the repository.

```sh
make docker-build
```

### Push container image

```sh
make docker-push
```
