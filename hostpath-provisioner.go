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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	filepath "path/filepath"
	"regexp"
	"strings"
	"syscall"

	yaml "gopkg.in/yaml.v3"

	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
)

const provisionerIdentityAnnotation = "hostpath/provisionerIdentity"

// Fetch provisioner name from environment variable HOSTPATH_PROVISIONER_NAME
// if not set uses default hostpath name
func GetProvisionerName() string {
	provisionerName := os.Getenv("HOSTPATH_PROVISIONER_NAME")
	if provisionerName == "" {
		provisionerName = "hostpath"
	}
	return provisionerName
}

type HostPathProvisioner struct {
	// The directory to create PV-backing directories in
	PVDir string

	// Identity of this hostPathProvisioner, set to node's name. Used to identify
	// "this" provisioner's PVs.
	Identity string

	// The annotation name to look for within PVCs when a specific location is
	// desired within the path tree
	HostPathAnnotation string

	// The annotation name to look for within PVCs which contains the regex
	// with which to parse out the PVC ID from the PVC Name
	PvcIdPatternAnnotation string

	// The annotation name to look for within PVCs which contains the replacement
	// string (i.e. with $1, $2, etc) in order to produce the desired PVC ID value
	PvcIdReplaceAnnotation string

	// The directory at which the created volumes will be accessible to the pod
	HostPathMount string
}

// NewHostPathProvisioner creates a new hostpath provisioner
func NewHostPathProvisioner() controller.Provisioner {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		klog.Fatal("env variable NODE_NAME must be set so that this provisioner can identify itself")
		// If no nodename is given, use a default value
		nodeName = "hostpath-provisioner"
	}
	nodeHostPath := os.Getenv("NODE_HOST_PATH")
	if nodeHostPath == "" {
		nodeHostPath = "/hostPath"
	}
	nodeHostPathAnnotation := os.Getenv("NODE_HOST_PATH_ANNOTATION")
	if nodeHostPathAnnotation == "" {
		nodeHostPathAnnotation = "hostpath/location"
	}
	nodeHostPvcIdPatternAnnotation := os.Getenv("NODE_HOST_PVCID_PATTERN_ANNOTATION")
	if nodeHostPvcIdPatternAnnotation == "" {
		nodeHostPvcIdPatternAnnotation = "hostpath/pvcId-pattern"
	}
	nodeHostPvcIdReplaceAnnotation := os.Getenv("NODE_HOST_PVCID_REPLACE_ANNOTATION")
	if nodeHostPvcIdReplaceAnnotation == "" {
		nodeHostPvcIdReplaceAnnotation = "hostpath/pvcId-replace"
	}
	nodeHostPathMount := os.Getenv("NODE_HOST_PATH_MOUNT")
	if nodeHostPathMount == "" {
		nodeHostPathMount = "/hostPath"
	} else if !filepath.IsAbs(nodeHostPathMount) {
		klog.Warningf("The given NODE_HOST_PATH_MOUNT value [%s] must be an absolute path", nodeHostPathMount)
		nodeHostPathMount = "/hostPath"
	}
	result := HostPathProvisioner{
		PVDir:                  nodeHostPath,
		Identity:               nodeName,
		HostPathAnnotation:     nodeHostPathAnnotation,
		PvcIdPatternAnnotation: nodeHostPvcIdPatternAnnotation,
		PvcIdReplaceAnnotation: nodeHostPvcIdReplaceAnnotation,
		HostPathMount:          nodeHostPathMount,
	}
	yamlData, err := yaml.Marshal(result)
	if err == nil {
		klog.Infof("Initialized as follows:\n%s", yamlData)
	} else {
		klog.Fatalf("Failed to marshal the constructed object into YAML: %s", err)
	}
	return &result
}

var _ controller.Provisioner = &HostPathProvisioner{}

// Provision creates a storage asset and returns a PV object representing it.
func (p *HostPathProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	relativePath := options.PVName

	// Allow the use of an annotation to request a specific location within the
	// directory hierarchy. If the annotation isn't present, the original behavior
	// is preserved.
	if customPath, ok := options.PVC.Annotations[p.HostPathAnnotation]; ok {
		klog.Infof("Computing the host path for PVC %s/%s from the %s annotation: [%s]", options.PVC.Namespace, options.PVC.Name, p.HostPathAnnotation, customPath)

		// The default value if the hostpath annotation value is invalid
		relativePath = customPath

		// Cleanup the annotation value to remove leading slash (no abs path allowed),
		// double slashes, normalize . and .. components, and remove the trailing slash
		sep := string(os.PathSeparator)

		// Compute the PVC ID, which may need to be replaced into the hostPath. If it's not
		// provided via headers, use "${options.PVC.Name}" as the value.
		pvcId := options.PVC.Name

		// If we were given a pattern and a replacmement to parse the PVC Name to get an ID,
		// use them ... but only use the result if it's a non-empty string
		pvcIdPattern, patternOk := options.PVC.Annotations[p.PvcIdPatternAnnotation]
		pvcIdReplace, replaceOk := options.PVC.Annotations[p.PvcIdReplaceAnnotation]
		if patternOk && replaceOk {
			klog.Infof("\tpvcId Pattern: [%s]", pvcIdPattern)
			klog.Infof("\tpvcId Replace: [%s]", pvcIdReplace)
			klog.Infof("\tpvcId Value  : [%s]", pvcId)
			regex, err := regexp.Compile(pvcIdPattern)
			if err != nil {
				klog.Warningf("The pvcId pattern [%s] is not valid: %s", pvcIdPattern, err)
			} else {
				replacement := strings.TrimSpace(regex.ReplaceAllString(pvcId, pvcIdReplace))
				klog.Infof("\tpvcId Result : [%s]", replacement)
				if replacement != "" {
					pvcId = replacement
				}
			}
		} else {
			if !patternOk {
				klog.Infof("No %s annotation for PVC %s/%s, can't apply regex transformation", p.PvcIdPatternAnnotation, options.PVC.Namespace, options.PVC.Name)
			}
			if !replaceOk {
				klog.Infof("No %s annotation for PVC %s/%s, can't apply regex transformation", p.PvcIdReplaceAnnotation, options.PVC.Namespace, options.PVC.Name)
			}
		}

		// Perform a verbatim value replacement on the ${pvcId} placeholder
		customPath = strings.ReplaceAll(customPath, "${pvcId}", pvcId)

		customPath = filepath.Clean(customPath)
		customPath = strings.TrimPrefix(customPath, sep)
		customPath = strings.TrimSuffix(customPath, sep)
		if (customPath != ".") && (customPath != "") {
			relativePath = customPath
		}
	} else {
		klog.Infof("No %s annotation for PVC %s/%s, will use the default path: [%s]", p.HostPathAnnotation, options.PVC.Namespace, options.PVC.Name, relativePath)
	}
	hostPath := path.Join(p.PVDir, relativePath)
	volumeName := options.PVName

	klog.Infof("Provisioning volume %s from PVC %s/%s at host path [%s]", volumeName, options.PVC.Namespace, options.PVC.Name, hostPath)
	if err := os.MkdirAll(path.Join(p.HostPathMount, relativePath), 0775); err != nil {
		klog.Fatalf("\tProvisioning failed: %s", err)
		return nil, controller.ProvisioningFinished, err
	}

	volumeType := v1.HostPathDirectoryOrCreate
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeName,
			Annotations: map[string]string{
				provisionerIdentityAnnotation: p.Identity,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: hostPath,
					Type: &volumeType,
				},
			},
		},
	}

	return pv, controller.ProvisioningFinished, nil
}

// Delete removes the storage asset that was created by Provision represented
// by the given PV. The path is read directly from the PV object, to more transparently
// support the use of the hostPathAnnotation
func (p *HostPathProvisioner) Delete(ctx context.Context, volume *v1.PersistentVolume) error {
	ann, ok := volume.Annotations[provisionerIdentityAnnotation]
	if !ok {
		return errors.New("identity annotation not found on PV")
	}
	if ann != p.Identity {
		return &controller.IgnoredError{Reason: "identity annotation on PV does not match ours"}
	}

	// Handle the contingency that the path may already have
	// been deleted, and said deletion was interrupted, so
	// the deletion request was sent again by K8s ...

	hostPath := volume.Spec.PersistentVolumeSource.HostPath.Path
	klog.Infof("Removing the contents for volume %s at host path [%s]", volume.Name, hostPath)
	relPath, err := filepath.Rel(p.PVDir, hostPath)
	if err != nil {
		klog.Fatalf("\tFailed to relativize the host path: %s", err)
		return err
	}

	// First, rename the target path to the new "temporary-deletion" path
	// so we can delete it without fear of collision with any new volumes that
	// may be created which match this original volume's location (i.e. defend
	// against the delete-create/create-delete race).
	//
	// i.e.: .../${volumeLeafFolder} -> .../.deleted.${volumeLeafFolder}.${volume.UID}
	//
	// THEN fire off the deletion of the new, unique path so it can happen
	// at any time.
	//
	// Possibly add to the constructor the launching of a background task
	// finding all pending deletions in our root directory, and deleting them
	// in a background thread (if they're not already being deleted)
	//
	// This is only necessary for custom schemes that risk name collisions. However,
	// applying this algorithm universally makes it simpler to run the background
	// cleanup task to remove all pending volume data (does K8s already track this
	// pending cleanup and fire off the volume deletion again if needed?)
	fullPath := path.Join(p.HostPathMount, relPath)
	parentPath := path.Dir(fullPath)
	leafName := path.Base(fullPath)
	deleteLeafName := fmt.Sprintf(".deleted.%s.%s", leafName, volume.UID)
	fullDeletePath := path.Join(parentPath, deleteLeafName)

	// If the delete path already exists, then just continue deleting
	if _, err := os.Stat(fullDeletePath); err == nil {
		// The delete path already exists, so no rename needed
		klog.Warningf("\tResuming interrupted deletion of [%s]", fullDeletePath)
	} else {
		// Does the volume path exist?
		if _, err := os.Stat(fullPath); err != nil {
			// the volume's path doesn't exist, so don't delete anything
			klog.Infof("\tThe volume path [%s] no longer exists, skipping the deletion", fullPath)
			return nil
		}

		// Do the rename thing ... this will yield a unique name
		if err := os.Rename(fullPath, fullDeletePath); err == nil {
			klog.Infof("\tRenamed the path [%s] to [%s] for race protection", fullPath, fullDeletePath)
		} else {
			klog.Fatalf("\tFailed to rename the path [%s] to [%s]: %s", fullPath, fullDeletePath, err)
			// The rename failed, so just nuke the original path ... :(
			fullDeletePath = fullPath
		}
	}

	klog.Infof("\tDeleting [%s] recursively...", fullDeletePath)
	if err := os.RemoveAll(fullDeletePath); err != nil {
		klog.Fatalf("\tFailed to remove the contents: %s", err)
		return err
	}
	klog.Infof("\tDeletion complete!")
	return nil
}

func main() {
	syscall.Umask(0)

	flag.Parse()
	flag.Set("logtostderr", "true")

	// Create an InClusterConfig and use it to create a client for the controller
	// to use to communicate with Kubernetes
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to create config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create client: %v", err)
	}

	// Create the provisioner: it implements the Provisioner interface expected by
	// the controller
	hostPathProvisioner := NewHostPathProvisioner()

	// Start the provision controller which will dynamically provision hostPath
	// PVs
	pc := controller.NewProvisionController(clientset, GetProvisionerName(), hostPathProvisioner)

	// Never stops.
	pc.Run(context.Background())
}
