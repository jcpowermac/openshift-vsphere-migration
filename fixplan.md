Task summary
- You have created this kubernetes controller to support the migration of openshift from one vSphere enviornment to another. It is not functioning properly. 

Expectations: follow these EXACTLY
- Build and run unit tests after EVERY code change
- Test driven development, use govmomi simulator for vSphere specific tests
- Expert level at golang
- Will run golint, so best practices are followed
- Document everything
- Re-use, clean, minimal, simple to understand code.
- Use openshift libraries and other repositories for code re-use as much as possible. 
  - https://github.com/openshift/library-go 
  - https://github.com/openshift/client-go
  - https://github.com/openshift/installer
  - https://github.com/openshift/machine-api-operator
  - https://github.com/kubernetes-sigs/cluster-api-provider-vsphere
  - https://github.com/openshift/cluster-control-plane-machine-set-operator


Fix:
- Rollback is poorly functioning. Renable CVO last.
- Implement better idempotency, we should NOT be reapply if we don't need to.
- Critically - MUST BE FIXED: 
  - CPMS - upon deletion of the controlplanemachineset the set goes inactive instead of being removed. Continue deleting CPMS object BUT you will need to set to change 
    spec.template.machines_v1beta1_machine_openshift_io.failureDomains.vsphere.name AND set spec.state back to Active. This should be done AFTER the worker machinesets have rolled out.
    to the new failure domain for the destination vcenter 
  - The machine set currently DOES NOT SET the correct networks, template, workspace - datacenter,datastore,folder, resourcePool or server - these values should be coming from the VSphereMigration.

