// +build !providerless

/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azure

import (
	"strings"
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-07-01/compute"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
)

// AttachDisk attaches a vhd to vm
// the vhd must exist, can be identified by diskName, diskURI, and lun.
func (ss *scaleSet) AttachDisk(isManagedDisk bool, diskName, diskURI string, nodeName types.NodeName, lun int32, cachingMode compute.CachingTypes, diskEncryptionSetID string) error {
	vmName := mapNodeNameToVMName(nodeName)
	ssName, instanceID, vm, err := ss.getVmssVM(vmName, cacheReadTypeDefault)
	if err != nil {
		return err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	disks := []compute.DataDisk{}
	if vm.StorageProfile != nil && vm.StorageProfile.DataDisks != nil {
		disks = filterDetachingDisks(*vm.StorageProfile.DataDisks)
	}
	if isManagedDisk {
		managedDisk := &compute.ManagedDiskParameters{ID: &diskURI}
		if diskEncryptionSetID == "" {
			if vm.StorageProfile.OsDisk != nil &&
				vm.StorageProfile.OsDisk.ManagedDisk != nil &&
				vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet != nil &&
				vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
				// set diskEncryptionSet as value of os disk by default
				diskEncryptionSetID = *vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet.ID
			}
		}
		if diskEncryptionSetID != "" {
			managedDisk.DiskEncryptionSet = &compute.DiskEncryptionSetParameters{ID: &diskEncryptionSetID}
		}
		disks = append(disks,
			compute.DataDisk{
				Name:         &diskName,
				Lun:          &lun,
				Caching:      compute.CachingTypes(cachingMode),
				CreateOption: "attach",
				ManagedDisk:  managedDisk,
			})
	} else {
		disks = append(disks,
			compute.DataDisk{
				Name: &diskName,
				Vhd: &compute.VirtualHardDisk{
					URI: &diskURI,
				},
				Lun:          &lun,
				Caching:      compute.CachingTypes(cachingMode),
				CreateOption: "attach",
			})
	}
	newVM := compute.VirtualMachineScaleSetVM{
		Sku:      vm.Sku,
		Location: vm.Location,
		VirtualMachineScaleSetVMProperties: &compute.VirtualMachineScaleSetVMProperties{
			HardwareProfile: vm.HardwareProfile,
			StorageProfile: &compute.StorageProfile{
				OsDisk:    vm.StorageProfile.OsDisk,
				DataDisks: &disks,
			},
		},
	}

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// Invalidate the cache right after updating
	defer ss.deleteCacheForNode(vmName)

	operationName := fmt.Sprintf("azureDisk - update(%s): vm(%s) - attach disk(%s, %s) with DiskEncryptionSetID(%s)", nodeResourceGroup, nodeName, diskName, diskURI, diskEncryptionSetID)
	klog.V(2).Infof(operationName)
	future, rerr := ss.VirtualMachineScaleSetVMsClient.UpdateFuture(ctx, nodeResourceGroup, ssName, instanceID, newVM, "attach_disk")
		
	if rerr != nil {
		detail := rerr.Error().Error()
		if strings.Contains(detail, errLeaseFailed) || strings.Contains(detail, errDiskBlobNotFound) {
			// if lease cannot be acquired or disk not found, immediately detach the disk and return the original error
			klog.Infof("azureDisk - err %s, try detach disk(%s, %s)", detail, diskName, diskURI)
			ss.DetachDisk(diskName, diskURI, nodeName)
		} else {
			defer ss.controllerCommon.vmLockMap.DeleteEntry(LunLockKey(string(nodeName), int(lun)))
		}
		return rerr.Error()
	}

	if future != nil {
		go func() {
			waitCtx, waitCtxCancel := getContextWithCancel()
			waitCtx = context.WithValue(waitCtx, "Operation", operationName)
			defer waitCtxCancel()
			defer ss.controllerCommon.vmLockMap.DeleteEntry(LunLockKey(string(nodeName), int(lun)))
			defer ss.deleteCacheForNode(vmName)
			_ = ss.VirtualMachineScaleSetVMsClient.FutureWaitForCompletion(waitCtx, future)
		}()
		
	}

	klog.V(2).Infof("azureDisk - attach disk(%s, %s) succeeded", diskName, diskURI)
	return nil
}

// DetachDisk detaches a disk from host
// the vhd can be identified by diskName or diskURI
func (ss *scaleSet) DetachDisk(diskName, diskURI string, nodeName types.NodeName) error {
	vmName := mapNodeNameToVMName(nodeName)
	ssName, instanceID, vm, err := ss.getVmssVM(vmName, cacheReadTypeDefault)
	if err != nil {
		return err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	disks := []compute.DataDisk{}
	if vm.StorageProfile != nil && vm.StorageProfile.DataDisks != nil {
		disks = filterDetachingDisks(*vm.StorageProfile.DataDisks)
	}
	bFoundDisk := false
	var lun int32
	for i, disk := range disks {
		if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
			(disk.Vhd != nil && disk.Vhd.URI != nil && diskURI != "" && strings.EqualFold(*disk.Vhd.URI, diskURI)) ||
			(disk.ManagedDisk != nil && diskURI != "" && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
			// found the disk
			klog.V(2).Infof("azureDisk - detach disk: name %q uri %q", diskName, diskURI)
			disks = append(disks[:i], disks[i+1:]...)
			bFoundDisk = true
			lun = *disk.Lun
			break
		}
	}

	if !bFoundDisk {
		// only log here, next action is to update VM status with original meta data
		klog.Errorf("detach azure disk: disk %s not found, diskURI: %s", diskName, diskURI)
	}
	if lun > 0 {
		ss.controllerCommon.vmLockMap.TryEntry(LunLockKey(string(nodeName), int(lun)))
	}

	newVM := compute.VirtualMachineScaleSetVM{
		Sku:      vm.Sku,
		Location: vm.Location,
		VirtualMachineScaleSetVMProperties: &compute.VirtualMachineScaleSetVMProperties{
			HardwareProfile: vm.HardwareProfile,
			StorageProfile: &compute.StorageProfile{
				OsDisk:    vm.StorageProfile.OsDisk,
				DataDisks: &disks,
			},
		},
	}

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// Invalidate the cache right after updating
	defer ss.deleteCacheForNode(vmName)

	operationName := fmt.Sprintf("azureDisk - update(%s): vm(%s) - detach disk(%s, %s)", nodeResourceGroup, nodeName, diskName, diskURI)
	klog.V(2).Infof(operationName)
	future, rerr := ss.VirtualMachineScaleSetVMsClient.UpdateFuture(ctx, nodeResourceGroup, ssName, instanceID, newVM, "detach_disk")

	if rerr != nil {
		defer ss.controllerCommon.vmLockMap.DeleteEntry(LunLockKey(string(nodeName), int(lun)))
		return rerr.Error()
	}

	if future != nil {
		go func() {
			waitCtx, waitCtxCancel := getContextWithCancel()
			waitCtx = context.WithValue(waitCtx, "Operation", operationName)
			defer waitCtxCancel()
			defer ss.controllerCommon.vmLockMap.DeleteEntry(LunLockKey(string(nodeName), int(lun)))
			defer ss.deleteCacheForNode(vmName)
			_ = ss.VirtualMachineScaleSetVMsClient.FutureWaitForCompletion(waitCtx, future)
		}()
	} else {
    defer ss.controllerCommon.vmLockMap.DeleteEntry(LunLockKey(string(nodeName), int(lun)))
	}
	return nil
}

// GetDataDisks gets a list of data disks attached to the node.
func (ss *scaleSet) GetDataDisks(nodeName types.NodeName, crt cacheReadType) ([]compute.DataDisk, error) {
	_, _, vm, err := ss.getVmssVM(string(nodeName), crt)
	if err != nil {
		return nil, err
	}

	if vm.StorageProfile == nil || vm.StorageProfile.DataDisks == nil {
		return nil, nil
	}

	return *vm.StorageProfile.DataDisks, nil
}
