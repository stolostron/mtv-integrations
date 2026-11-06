# Recovering a failed CNV HyperConverged install

This runbook covers managed clusters where the older CNV addon applied the HyperConverged (HCO) resource before `hco-webhook-service` was ready. This could cause HCO validation to fail and leave the HCO ClusterServiceVersion (CSV) in a failed state.

## Apply the gated addon manifests

Update the hub using the chart or `update_cleanup.sh`. For direct `oc apply`, apply `addons/cnv-addon/cnv-clustermanagementaddon.yaml` first; it contains both the ClusterManagementAddOn and its AddOnDeploymentConfig. Confirm the addon uses that config, then apply `cnv-addon-template.yaml`. Delete the old `binding-kubevirt-hyperconverged` PlacementBinding and `kubevirt-hyperconverged` Policy from `open-cluster-management-global-set`; otherwise the old hub policy remains active.

The ConfigurationPolicy in the CNV AddOnTemplate checks the spoke OperatorPolicy `kubevirt-hyperconverged-operator` in `open-cluster-management-policies` for `Compliant`. It then creates the missing CR from the HCO definition inside its own template. The AddOnDeploymentConfig bundled with `cnv-clustermanagementaddon.yaml` supplies the delimiter needed to preserve the managed policy's Go templates through AddOnTemplate rendering. If a cluster overrides the default AddOnDeploymentConfig, its override must define `HCO_TEMPLATE_START` as `{{`. OperatorPolicy compliance does not explicitly confirm that `hco-webhook-service` has ready endpoints.

The addon ManifestWork keeps an identity-only `ReadOnly` HCO entry to preserve tracking of existing CRs during ManifestWork updates. It does not create or modify HCO; the ConfigurationPolicy does that after the operator is compliant. An old `AppliedManifestWork` owner reference can remain on an existing HCO, so deleting the entire ManifestWork may still delete that CR.

## Recover the managed cluster

Run the following commands against the affected managed cluster. Set `SPOKE_CONTEXT` to the kubeconfig context for that cluster.

Before deleting HyperConverged, check the managed ConfigurationPolicy:

```bash
oc --context "$SPOKE_CONTEXT" get configurationpolicy kubevirt-hyperconverged-config \
  -n open-cluster-management-policies -o jsonpath='{.spec.remediationAction}'
```

Continue only if it reports `enforce`. The AddOnTemplate uses `CreateOnly`, so an existing `inform` ConfigurationPolicy is not updated automatically and will not recreate a deleted HyperConverged resource.

1. Remove the HyperConverged resource that failed validation:

   ```bash
   oc --context "$SPOKE_CONTEXT" delete hyperconverged kubevirt-hyperconverged \
     -n openshift-cnv --ignore-not-found
   ```

2. Check the HCO CSV:

   ```bash
   oc --context "$SPOKE_CONTEXT" get csv -n openshift-cnv
   ```

   If the HCO CSV is in `Failed` phase, delete that failed CSV and its subscription. The enforced OperatorPolicy should recreate the subscription:

   ```bash
   FAILED_HCO_CSV=REPLACE_WITH_FAILED_CSV_NAME
   oc --context "$SPOKE_CONTEXT" delete csv "$FAILED_HCO_CSV" -n openshift-cnv
   oc --context "$SPOKE_CONTEXT" delete subscription kubevirt-hyperconverged -n openshift-cnv
   ```

3. Wait for the OperatorPolicy to report `Compliant` and for the HCO CSV to reach `Succeeded`:

   ```bash
   oc --context "$SPOKE_CONTEXT" get operatorpolicy kubevirt-hyperconverged-operator \
     -n open-cluster-management-policies
   oc --context "$SPOKE_CONTEXT" get csv -n openshift-cnv
   ```

4. Check that the addon ConfigurationPolicy recreated HyperConverged:

   ```bash
   oc --context "$SPOKE_CONTEXT" get hyperconverged kubevirt-hyperconverged -n openshift-cnv
   ```

If the OperatorPolicy remains noncompliant or the CSV fails again, inspect the OperatorPolicy status and the CSV install events before retrying the recovery.
