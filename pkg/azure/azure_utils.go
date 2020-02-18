// +build !providerless

/*
Copyright 2019 The Kubernetes Authors.

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
	"sync"
	"fmt"
	"k8s.io/klog"
)

// LunLockKey - returns node+lun key for lockMap
func LunLockKey(nodeName string, lun int) string {
  return fmt.Sprintf("%s-lun-%d", nodeName, lun)
}

// lockMap used to lock on entries
type lockMap struct {
	sync.Mutex
	mutexMap map[string]*sync.Mutex
}

// NewLockMap returns a new lock map
func newLockMap() *lockMap {
	return &lockMap{
		mutexMap: make(map[string]*sync.Mutex),
	}
}

// LockEntry acquires a lock associated with the specific entry
func (lm *lockMap) LockEntry(entry string) {
	lm.Lock()
	klog.V(4).Infof("LockEntry %s", entry)
	// check if entry does not exists, then add entry
	if _, exists := lm.mutexMap[entry]; !exists {
		lm.addEntry(entry)
	}

	lm.Unlock()
	lm.lockEntry(entry)
}

// TryEntry - checks if lm.mutexMap[entry] is empty, inserts the entry and return true
//            returns false if if lm.mutexMap[entry] is not empty
func (lm *lockMap) TryEntry(entry string) bool {
	lm.Lock()
	defer lm.Unlock()
	if _, exists := lm.mutexMap[entry]; exists {
		klog.V(4).Infof("TryEntry %s - already locked", entry)
		return false
	}
	lm.addEntry(entry)
	klog.V(4).Infof("TryEntry %s - success", entry)
	return true
}

// DeleteEntry - checks if lm.mutexMap[entry] exists and delete it 
//            returns false if if lm.mutexMap[entry] does not exists
func (lm *lockMap) DeleteEntry(entry string) bool {
	lm.Lock()
	defer lm.Unlock()
	if _, exists := lm.mutexMap[entry]; !exists {
		klog.V(4).Infof("DeleteEntry %s - entry does not exists", entry)
    return false
	}
	delete(lm.mutexMap, entry)
	klog.V(4).Infof("DeleteEntry %s - deleted", entry)
	return true
}

// UnlockEntry release the lock associated with the specific entry
func (lm *lockMap) UnlockEntry(entry string) {
	lm.Lock()
	defer lm.Unlock()

	if _, exists := lm.mutexMap[entry]; !exists {
		klog.V(4).Infof("UnlockEntry %s - entry does not exists", entry)
		return
	}
	lm.unlockEntry(entry)
	klog.V(4).Infof("UnlockEntry %s - unlocked", entry)
}

func (lm *lockMap) addEntry(entry string) {
	lm.mutexMap[entry] = &sync.Mutex{}
}

func (lm *lockMap) lockEntry(entry string) {
	lm.mutexMap[entry].Lock()
}

func (lm *lockMap) unlockEntry(entry string) {
	lm.mutexMap[entry].Unlock()
}
