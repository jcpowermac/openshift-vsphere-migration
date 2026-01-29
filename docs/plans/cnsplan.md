Task summary
- You have created this kubernetes controller to support the migration of openshift from one vSphere environment to another.
  We now need to plan how to migrate vSphere CSI Persistent Volumes (Container Native Storage, First Class Disk) from
  a source to destination vCenter. 

Expectations: 
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
    - https://github.com/openshift/vmware-vsphere-csi-driver

Research possibilities:
- There was previous work when moving a intree persistent volume to out-of-tree, external vSphere CSI. Research cnsvspherevolumemigrations.cns.vmware.com
- VMDK relocation
  - https://github.com/vmware/govmomi/blob/55b758a91203cc2fe7c53a6cea06ada86fabdccb/object/virtual_machine.go Relocate().
  - For each persistent volume we would need to know the pod and then the parent resource to determine how to scale down.
  - For each persistent volume we need to know the path to the VMDK
  - Create a dummy virtual machine, attach the VMDK, use Relocate to perform a cross-vcenter vmotion and storage vmotion from source to destination.
  - This would be a new phase between RecreateCPMS and ScaleOldMachines
  - Review this PR: https://github.com/openshift/vmware-vsphere-csi-driver-operator/pull/242/files to determine methods of adding the VMDK to 
    the destination CNS.
  - Modify the persistent volume to use the copied disk on the destination. 