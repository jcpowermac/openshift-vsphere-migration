Task summary
- You have created this kubernetes controller to support the migration of openshift from one vSphere environment to another.
  There are issues with the CSI persistent volume migration

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

Problems:
- vcenter migration task error: csi-migration-jcallen2-x7x2g-pvc-4e0f Authenticity of the host's SSL certificate is not verified.
- The logging indicated that the virtual machine was migrated but it was NOT. We know this because of the error from vCenter. 
  This is a serious problem! We cannot lose customer's data. Logging must be improved if migration failure occur. See logs below

   I0129 16:40:27.166015 4166032 fcd.go:76] "Retrieved FCD" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" namespace="openshift-co
   nfig" id="fa8b9dc5-0af3-45e2-b0f2-c91be006fcfc" name="pvc-2ee94ec0-dad6-4262-aeb9-2b36d9c2207a" path="[Datastore] fcd/_00dc/d08c4b72d76c47aca782956f21171e35.vm
   dk"
   I0129 16:40:27.166038 4166032 phase_12_5_migrate_csi_volumes.go:340] "Found FCD" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration"
   namespace="openshift-config" id="fa8b9dc5-0af3-45e2-b0f2-c91be006fcfc" name="pvc-2ee94ec0-dad6-4262-aeb9-2b36d9c2207a" path="[Datastore] fcd/_00dc/d08c4b72d76
   c47aca782956f21171e35.vmdk"
   I0129 16:40:27.219386 4166032 vm_relocate.go:67] "Creating dummy VM for CSI volume migration" key="openshift-config/vcenter-120-migration" migration="vcenter-1
   20-migration" namespace="openshift-config" name="csi-migration-jcallen2-x7x2g-pvc-2ee9" datacenter="nested-dc01" cluster="/nested-dc01/host/nested-cluster01"
   I0129 16:40:28.454008 4166032 vm_relocate.go:147] "Successfully created dummy VM" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration
   " namespace="openshift-config" name="csi-migration-jcallen2-x7x2g-pvc-2ee9" moref="vm-223"
   I0129 16:40:28.722113 4166032 fcd.go:186] "Attaching FCD to VM" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" namespace="opens
   hift-config" fcdID="fa8b9dc5-0af3-45e2-b0f2-c91be006fcfc" vm=""
   I0129 16:40:29.156509 4166032 fcd.go:193] "Successfully attached FCD to VM" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" name
   space="openshift-config" fcdID="fa8b9dc5-0af3-45e2-b0f2-c91be006fcfc" vm=""
   I0129 16:40:29.210029 4166032 vm_relocate.go:187] "Relocating VM to target vCenter" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migrati
   on" namespace="openshift-config" vm="" targetVCenter="https://vcenter-120.ci.ibmc.devcluster.openshift.com/sdk" targetDatacenter="wldn-120-DC"
   I0129 16:40:29.939702 4166032 vm_relocate.go:236] "Starting VM relocation task" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration"
   namespace="openshift-config"
   I0129 16:41:00.049686 4166032 vm_relocate.go:154] "Deleting dummy VM" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" namespace=
   "openshift-config" name=""
   I0129 16:41:00.547921 4166032 vm_relocate.go:180] "Successfully deleted dummy VM" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration
   " namespace="openshift-config" name=""
   I0129 16:41:00.547953 4166032 workloads.go:119] "Restoring workloads" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" namespace=
   "openshift-config" count=1
   I0129 16:41:00.547962 4166032 workloads.go:123] "Restoring workload" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migration" kind="Deplo
   yment" name="test-deployment-1" namespace="default" replicas=1
   I0129 16:41:00.669601 4166032 workloads.go:151] "Successfully restored all workloads" key="openshift-config/vcenter-120-migration" migration="vcenter-120-migra
   tion" namespace="openshift-config"