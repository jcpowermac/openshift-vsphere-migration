
Task summary
- You have created this kubernetes controller to support the migration of openshift from one vSphere environment to another.
  There are a number of changes that need to be done.

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
- Naming conventions, change objects to represent `migration-vmware-cloud-foundation`
- create namespace manifest in ./deploy
- if folder is not defined in `VmwareCloudFoundationMigration` spec.failuredomain.topology generate it instead based on the infraid. This should be in the form /<datacenter>/vm/<infraid>
- validate the destination vcenter that the topology objects exist in vcenter.  
- more pronounced logging message when rollback is initiated, it is very hard to find this in the log when it first starts 
- generate an updated installer metadata.json for installer destroy - https://github.com/openshift/installer/blob/main/pkg/types/vsphere/metadata.go
- Place manifests in a namespace, crd should be namespaced. Do not add objects to openshift-config
- do not backup cpms, there is no need to do this.
- do not backup worker machineset, there is no need to do this.
- Once the destination worker machineset is ready, and the phase to remove worker machineset needs to happen before modifying the infrastructure spec and cloud-config.




