# Dynamic Provisioning of Kubernetes HostPath Volumes

A tool to dynamically provision Kubernetes HostPath Volumes in single-node Kubernetes cluster as [kind](https://github.com/kubernetes-sigs/kind).

It is based on [kubernetes-sigs/sig-storage-lib-external-provisioner/hostpath-provisioner](https://github.com/kubernetes-sigs/sig-storage-lib-external-provisioner/tree/master/examples/hostpath-provisioner) example project.

## TL;DR

```bash
# install dynamic hostpath provisioner Helm chart
helm repo add rimusz https://charts.rimusz.net
helm repo update
helm upgrade --install hostpath-provisioner --namespace kube-system rimusz/hostpath-provisioner
```

```bash
# create a test-pvc and a pod writing to it
kubectl create -f https://raw.githubusercontent.com/rimusz/hostpath-provisioner/master/deploy/test-claim.yaml
kubectl create -f https://raw.githubusercontent.com/rimusz/hostpath-provisioner/master/deploy/test-pod.yaml

# docker exec to kind node
docker exec -it container_id bash
# expect a folder to exist on your host
ls -alh /mnt/hostpath/pvc-*/

kubectl delete test-pod
kubectl delete pvc hostpath-pvc

# expect the folder to be removed from your host
ls -alh /mnt/hostpath/pvc-*/
```

## Additional Environment Variables

 `NODE_HOST_PATH` - Use this to set a custom directory as your hostpath mount point. If blank, uses default `/mnt/hostpath`
