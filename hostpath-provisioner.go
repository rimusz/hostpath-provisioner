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
	pvDir string

	// Identity of this hostPathProvisioner, set to node's name. Used to identify
	// "this" provisioner's PVs.
	identity string

	// The annotation name to look for within PVCs when a specific location is
	// desired within the path tree
	hostPathAnnotation string

	// The annotation name to look for within PVCs which contains the regex
	// with which to parse out the PVC ID from the PVC Name
	pvcIdPatternAnnotation string

	// The annotation name to look for within PVCs which contains the replacement
	// string (i.e. with $1, $2, etc) in order to produce the desired PVC ID value
	pvcIdReplaceAnnotation string

	// The directory at which the created volumes will be accessible to the pod
	hpMount string
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
		pvDir:                  nodeHostPath,
		identity:               nodeName,
		hostPathAnnotation:     nodeHostPathAnnotation,
		pvcIdPatternAnnotation: nodeHostPvcIdPatternAnnotation,
		pvcIdReplaceAnnotation: nodeHostPvcIdReplaceAnnotation,
		hpMount:                nodeHostPathMount,
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
	if customPath, ok := options.PVC.Annotations[p.hostPathAnnotation]; ok {
		klog.Infof("Computing the host path for PVC %s/%s from the %s annotation: [%s]", options.PVC.Namespace, options.PVC.Name, p.hostPathAnnotation, customPath)

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
		pvcIdPattern, patternOk := options.PVC.Annotations[p.pvcIdPatternAnnotation]
		pvcIdReplace, replaceOk := options.PVC.Annotations[p.pvcIdReplaceAnnotation]
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
				klog.Infof("No %s annotation for PVC %s/%s, can't apply regex transformation", p.pvcIdPatternAnnotation, options.PVC.Namespace, options.PVC.Name)
			}
			if !replaceOk {
				klog.Infof("No %s annotation for PVC %s/%s, can't apply regex transformation", p.pvcIdReplaceAnnotation, options.PVC.Namespace, options.PVC.Name)
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
		klog.Infof("No %s annotation for PVC %s/%s, will use the default path: [%s]", p.hostPathAnnotation, options.PVC.Namespace, options.PVC.Name, relativePath)
	}
	hostPath := path.Join(p.pvDir, relativePath)
	volumeName := options.PVName

	klog.Infof("Provisioning volume %s from PVC %s/%s at host path [%s]", volumeName, options.PVC.Namespace, options.PVC.Name, hostPath)
	if err := os.MkdirAll(path.Join(p.hpMount, relativePath), 0775); err != nil {
		klog.Fatalf("\tProvisioning failed: %s", err)
		return nil, controller.ProvisioningFinished, err
	}

	volumeType := v1.HostPathDirectoryOrCreate
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeName,
			Annotations: map[string]string{
				provisionerIdentityAnnotation: p.identity,
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
	if ann != p.identity {
		return &controller.IgnoredError{Reason: "identity annotation on PV does not match ours"}
	}

	hostPath := volume.Spec.PersistentVolumeSource.HostPath.Path
	klog.Infof("Removing the contents for volume %s at host path [%s]", volume.Name, hostPath)
	relPath, err := filepath.Rel(p.pvDir, hostPath)
	if err != nil {
		klog.Fatalf("\tFailed to relativize the host path: %s", err)
		return err
	}
	if err := os.RemoveAll(path.Join(p.hpMount, relPath)); err != nil {
		klog.Fatalf("\tFailed to remove the contents: %s", err)
		return err
	}

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
