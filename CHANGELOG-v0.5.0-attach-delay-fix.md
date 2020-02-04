## changes to fix attach delay issues

Issue: https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/269

*Solution:*
- on ControllerPublishVolume we do not wait for AzureApi to complete so Node object is populated with disk immediately
- on NodeStageVolume we wait until scsi device is attached to the node, so kubelet will not spin on exponensial backoff

##### changes 
1. Move k8s.io/legacy-cloud-providers/azure into this repo (sigs.k8s.io/azuredisk-csi-driver/pkg/azure) and change imports
2. remove WaitForCompletion on call AzureVMSS attach_disk () in pkg/azure/azure_client.go - func (az *azVirtualMachineScaleSetVMsClient) Update
3. on findDiskAndLun (pkg/nodeserver.go) we wait with 2m timeout until device is added to the node

