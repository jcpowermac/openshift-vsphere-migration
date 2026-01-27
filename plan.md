ultrathink

Task
- Create a kubernetes (openshift) controller to support the migration of a OpenShift cluster running on vSphere from one vCenter to another.

Expectations
- Test driven development, use govmomi simulator for vSphere specific tests
- Expert level at golang
- Will run golint, so best practices are followed
- Document everything
- Re-use, clean, minimal, simple to understand code.

Implementation
- SOAP and REST to vCenter should be extensively logged. 
- Use existing libraries for kube controllers https://github.com/openshift/library-go/tree/master/pkg/controller
-. A secret will be needed for the vCenter username and password. Follow existing designs
- CRD:
  1. Name: vspheremigrationinfra.config.openshift.io, vmi for short
  2. The CRD should be namespaced
  2. Spec field:
  - Design should follow existing infrastructure spec https://github.com/openshift/api/blob/master/config/v1/types_infrastructure.go#L1646
  - There should be a state field to control controller.  
  3. Status field should contain the history of the steps to complete the migration with proper logging

- Steps:
  1. Disable cluster version operator by scaling down the deployment
  2. Modify the existing infrastructure CRD allowing changes to the number of vCenters - https://github.com/openshift/api/blob/606bd613f9f7b6965f1d087b2fce8227aa49ceca/config/v1/types_infrastructure.go#L1657-L1662
  3. Backup the control plane machine set, delete the existing.
  4. Modify all vCenter authentication secrets, adding the new vCenter
  5. Modify the current and new vCenter, add openshift-zone, openshift-region tag categories. If the infrastructure spec only contains a single failure domain use a generic tag name of migration region and zone. Attach tag to new vCenter datacenter (openshift-region) and cluster (openshift-zone)
  6. Modify the new vCenter adding a vm folder with the infrastructure id.
  7. Modify the infrastructure spec from fields provided by `vmi` spec.
  8. Modify the cloud-provider-config openshift-config for new vCenter
  9. Restart vSphere specific pods, machine api, cloud-controller-manager, vsphere csi driver operator
  10. Monitor logs of the pods, if failures STOP. Extensively logging in controller and status fields as to the cause and where to look for more information
  11. Create a new machine set with configuration from `vmi`, monitor and confirm rollout.
  12. Recreate CPMS configuration with new vCenter zone. Monitor rollout.
  13. Scale down machine set from old vcenter
  14. Remove old vcenter from all configurations, restart pods, monitor








