# Recovering a failed CNV HyperConverged install

This runbook covers managed clusters where the older CNV addon applied the HyperConverged (HCO) resource before `hco-webhook-service` was ready. This could cause HCO validation to fail and leave the HCO ClusterServiceVersion (CSV) in a failed state.

## Apply the gated addon manifests

Update the hub first so the addon ManifestWork no longer recreates HyperConverged directly and the dependent hub Policy is installed. Apply the updated chart, the manifests under `addons/cnv-addon/`, or run `update_cleanup.sh` from the repository root.

The hub Policy waits for the spoke OperatorPolicy `kubevirt-hyperconverged-operator` in `open-cluster-management-policies` to report `Compliant`. It then enforces HyperConverged through a ConfigurationPolicy. The hub Policy does not define a second OperatorPolicy.

## Recover the managed cluster

Run the following commands against the affected managed cluster. Set `SPOKE_CONTEXT` to the kubeconfig context for that cluster.

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

4. Confirm the webhook Service has ready endpoints, then check that the dependent hub Policy recreated HyperConverged:

   ```bash
   oc --context "$SPOKE_CONTEXT" get endpoints hco-webhook-service -n openshift-cnv
   oc --context "$SPOKE_CONTEXT" get hyperconverged kubevirt-hyperconverged -n openshift-cnv
   ```

If the OperatorPolicy remains noncompliant or the CSV fails again, inspect the OperatorPolicy status and the CSV install events before retrying the recovery.
