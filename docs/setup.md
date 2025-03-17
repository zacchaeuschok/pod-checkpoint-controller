# **Container Checkpoint System - End-to-End Deployment Guide**

## **1. Build Components Locally**
Since everything is being built locally, compile the controller and sidecar.

```bash
# Build the controller binary
go build -o bin/external-checkpointer ./cmd/main.go

# Build the sidecar binary
go build -o bin/sidecar ./cmd/sidecar/main.go
```

Verify that the binaries exist:

```bash
ls -lh bin/
```

Expected output:
```
-rwxr-xr-x  1 user  staff  12M Feb 21 12:00 external-checkpointer
-rwxr-xr-x  1 user  staff  10M Feb 21 12:00 sidecar
```

## **2. Build Container Images for CRI-O Runtime**
Build the container images for both the controller and sidecar components with the correct names:

```bash
# Build the controller image with the exact name expected in manager.yaml
sudo buildah bud -t controller:latest .

# Build the sidecar image
sudo buildah bud -t external-checkpointer-sidecar:latest -f Dockerfile.sidecar .
```

Verify that the images were created:

```bash
sudo buildah images | grep -E 'controller|external-checkpointer'
```

Expected output:
```
localhost/controller                    latest    abc123def456   About a minute ago   57.9MB
localhost/external-checkpointer-sidecar latest    def456abc789   About a minute ago   57.9MB
```

## **3. Make Images Available to CRI-O**
Since you're using CRI-O as your container runtime, you need to make the locally built images available to it:

```bash
# Push the images directly to CRI-O storage
sudo buildah push controller:latest oci:/var/lib/containers/storage:controller:latest
sudo buildah push external-checkpointer-sidecar:latest oci:/var/lib/containers/storage:external-checkpointer-sidecar:latest
```

Verify the images are available in CRI-O:

```bash
sudo crictl images | grep -E 'controller|external-checkpointer'
```

## **4. Deploy Custom Resource Definitions (CRDs)**
The CRDs define `ContainerCheckpoint` and `ContainerCheckpointContent` as Kubernetes resources.

```bash
kubectl apply -f config/crd/bases/checkpointing.zacchaeuschok.github.io_containercheckpoints.yaml
kubectl apply -f config/crd/bases/checkpointing.zacchaeuschok.github.io_containercheckpointcontents.yaml
```

Verify that the CRDs are installed:

```bash
kubectl get crds | grep checkpoint
```

Expected output:
```
containercheckpoints.checkpointing.zacchaeuschok.github.io
containercheckpointcontents.checkpointing.zacchaeuschok.github.io
```

## **5. Deploy the Controller Manager**
Apply RBAC and deploy the controller.

```bash
# Deploy RBAC for the controller
kubectl apply -f config/rbac/

# Deploy the controller manager
kubectl apply -k config/default/
```

Verify the deployment:

```bash
kubectl get pods -n external-checkpointer-system
```

If the controller is not running, check logs:

```bash
kubectl logs -l control-plane=controller-manager -n external-checkpointer-system
```

### **Troubleshooting ImagePullBackOff Issues with CRI-O**
If your pods are in `ImagePullBackOff` status, ensure:

1. The images are built with the exact names expected in the deployment files:
   - `controller:latest` for the main controller
   - `external-checkpointer-sidecar:latest` for the sidecar

2. The images are properly imported into CRI-O:
   ```bash
   sudo crictl images | grep -E 'controller|external-checkpointer'
   ```

3. The `imagePullPolicy` is set to `IfNotPresent` or `Never` in the deployment YAML files:
   ```bash
   grep -r "imagePullPolicy" config/
   ```

4. If needed, edit the deployment directly:
   ```bash
   kubectl edit deployment controller-manager -n external-checkpointer-system
   ```
   
   Look for the `image` and `imagePullPolicy` fields and update them as needed.

## **6. Deploy the Checkpoint Sidecar**
The sidecar runs on each node as a DaemonSet to handle local checkpoint requests.

```bash
# Deploy RBAC for the sidecar
kubectl apply -f config/sidecar/rbac.yaml
kubectl apply -f config/sidecar/service_account.yaml

# Deploy the DaemonSet
kubectl apply -f config/sidecar/sidecar_daemonset.yaml
```

Verify that the DaemonSet is running on all nodes:

```bash
kubectl get daemonset container-checkpoint-sidecar -n kube-system
```

Check logs for any issues:

```bash
kubectl logs -n kube-system -l app=container-checkpoint-sidecar
```

### **Troubleshooting Sidecar Deployment with CRI-O**
If the sidecar pods are not running:

1. Verify the image is available in CRI-O:
   ```bash
   sudo crictl images | grep external-checkpointer-sidecar
   ```

2. Check the DaemonSet configuration:
   ```bash
   kubectl describe daemonset container-checkpoint-sidecar -n kube-system
   ```

3. Edit the DaemonSet if needed:
   ```bash
   kubectl edit daemonset container-checkpoint-sidecar -n kube-system
   ```

## **7. Create a Checkpoint Request**
Define a checkpoint request as a Kubernetes resource.

```yaml
# checkpoint.yaml
apiVersion: checkpointing.zacchaeuschok.github.io/v1alpha1
kind: ContainerCheckpoint
metadata:
  name: my-checkpoint
  namespace: default
spec:
  podName: my-pod
  namespace: default
  container: my-container
```

Apply the checkpoint request:

```bash
kubectl apply -f checkpoint.yaml
```

Verify the checkpoint status:

```bash
kubectl get containercheckpoint my-checkpoint -o yaml
```

Expected status:
- **If successful**:
  ```yaml
  status:
    state: Completed
    message: "Checkpoint completed successfully"
  ```
- **If failed**:
  ```yaml
  status:
    state: Failed
    message: "<error details>"
  ```

## **8. Debugging and Troubleshooting**
### **If the Controller is not running**
Check if the controller Pod is running:

```bash
kubectl get pods -n external-checkpointer-system
```

If the controller is crashing, check logs:

```bash
kubectl logs -n external-checkpointer-system \
  deployment/external-checkpointer-controller-manager
```

### **If the Sidecar is not responding**
Ensure the sidecar is running:

```bash
kubectl get pods -n kube-system -l app=container-checkpoint-sidecar
```

If logs show Kubelet API errors, check direct API access:

```bash
curl -k --cert /var/run/kubernetes/client-admin.crt \
    --key /var/run/kubernetes/client-admin.key \
    -X POST "https://localhost:10250/checkpoint/default/my-pod/my-container"
```

## **9. Clean Up**
To remove all components:

```bash
kubectl delete -f config/crd/bases/
kubectl delete -f config/rbac/
kubectl delete -k config/default/
kubectl delete -f config/sidecar/
```

Verify that everything is removed:

```bash
kubectl get all -n external-checkpointer-system
kubectl get daemonset container-checkpoint-sidecar -n kube-system
```

## **10. Example Usage Guide: Cross-Node Migration**

This section demonstrates how to use the checkpoint controller to migrate a stateful application between nodes.

For convenience, all example files are available in the `samples/` directory. You can use them directly:

```bash
# Deploy the sample application
kubectl apply -f samples/sample-app.yaml

# Create a checkpoint
kubectl apply -f samples/app-checkpoint.yaml

# Delete the original application
kubectl delete -f samples/sample-app.yaml

# Restore the application on a different node
kubectl apply -f samples/restored-app-node2.yaml
```

### **Step 1: Deploy a Sample Application on Node 1**

First, let's deploy a simple stateful application that we can checkpoint:

```yaml
# samples/sample-app.yaml
apiVersion: v1
kind: Pod
metadata:
  name: stateful-app
  namespace: default
spec:
  nodeSelector:
    kubernetes.io/hostname: node1  # Ensure it runs on node1
  containers:
  - name: counter
    image: busybox
    command: ["/bin/sh", "-c"]
    args:
    - |
      counter=0
      while true; do
        echo "Counter: $counter"
        counter=$((counter+1))
        sleep 10
      done
```

Apply the sample application:

```bash
kubectl apply -f samples/sample-app.yaml
```

Verify the pod is running on node1:

```bash
kubectl get pod stateful-app -o wide
```

### **Step 2: Create a Checkpoint**

Now, create a checkpoint for the container:

```yaml
# samples/app-checkpoint.yaml
apiVersion: checkpointing.zacchaeuschok.github.io/v1alpha1
kind: ContainerCheckpoint
metadata:
  name: counter-checkpoint
  namespace: default
spec:
  podName: stateful-app
  namespace: default
  container: counter
```

Apply the checkpoint request:

```bash
kubectl apply -f samples/app-checkpoint.yaml
```

### **Step 3: Monitor the Checkpoint Process**

Check the status of the checkpoint:

```bash
kubectl get containercheckpoint counter-checkpoint -o yaml
```

Once the checkpoint is completed, you'll see the ContainerCheckpointContent resource created:

```bash
kubectl get containercheckpointcontent counter-checkpoint -o yaml
```

Verify that the checkpoint data has been stored:

```bash
# This command should be run on node1
kubectl exec -it $(kubectl get pod -l app=container-checkpoint-sidecar -o jsonpath='{.items[0].metadata.name}' -n kube-system) -n kube-system -- ls -l /var/lib/kubelet/checkpoints/
```

### **Step 4: Delete the Original Application**

Delete the original pod:

```bash
kubectl delete pod stateful-app
```

### **Step 5: Restore the Application on Node 2**

Create a new pod on a different node that will restore from the checkpoint:

```yaml
# samples/restored-app-node2.yaml
apiVersion: v1
kind: Pod
metadata:
  name: restored-app
  namespace: default
  annotations:
    checkpointing.zacchaeuschok.github.io/restore-from: counter-checkpoint
spec:
  nodeSelector:
    kubernetes.io/hostname: node2  # Ensure it runs on node2
  containers:
  - name: counter
    image: busybox
    command: ["/bin/sh", "-c"]
    args:
    - |
      counter=0
      while true; do
        echo "Counter: $counter"
        counter=$((counter+1))
        sleep 10
      done
```

Apply the restored application:

```bash
kubectl apply -f samples/restored-app-node2.yaml
```

Verify the pod is running on node2:

```bash
kubectl get pod restored-app -o wide
```

### **Step 6: Verify the Cross-Node Restoration**

Check the logs to verify that the counter continues from where it left off, even though it's now running on a different node:

```bash
kubectl logs restored-app
```

You should see that the counter value starts from the value it had when the checkpoint was created, rather than from zero, demonstrating successful cross-node migration.

## **Validation Checklist**
- The **controller manager is running**
  ```bash
  kubectl get pods -n external-checkpointer-system
  ```
- The **sidecar DaemonSet is running on all nodes**
  ```bash
  kubectl get daemonset container-checkpoint-sidecar -n kube-system
  ```
- The **CRDs are recognized**
  ```bash
  kubectl get crds | grep checkpoint
  ```
- The **Kubelet API is accessible**
  ```bash
  curl -k --cert /var/run/kubernetes/client-admin.crt \
      --key /var/run/kubernetes/client-admin.key \
      -X POST "https://localhost:10250/checkpoint/default/my-pod/my-container"
  ```

This sequence ensures that the Container Checkpoint system is built, deployed, and verified correctly.

# Pod Checkpoint Controller Architecture

This document provides an overview of the Pod Checkpoint Controller architecture and how its components interact.

## Architecture Diagram

```
+---------------------------+     +---------------------------+
|                           |     |                           |
|  Kubernetes Control Plane |     |  Node                     |
|                           |     |                           |
|  +---------------------+  |     |  +---------------------+  |
|  |                     |  |     |  |                     |  |
|  |  Controller Manager |  |     |  |  Checkpoint         |  |
|  |  +---------------+  |  |     |  |  Sidecar            |  |
|  |  | Checkpoint    |  |  |     |  |  (DaemonSet)        |  |
|  |  | Controller    |<-|--|-----|->|                     |  |
|  |  +---------------+  |  |     |  |                     |  |
|  |  | Metrics Server|  |  |     |  |                     |  |
|  |  +---------------+  |  |     |  |                     |  |
|  |  | Health Probes |  |  |     |  |                     |  |
|  |  +---------------+  |  |     |  |                     |  |
|  +----------+----------+  |     |  +----------+----------+  |
|             |             |     |             |             |
|             v             |     |             v             |
|  +---------------------+  |     |  +---------------------+  |
|  |                     |  |     |  |                     |  |
|  |  Kubernetes API     |  |     |  |  Kubelet            |  |
|  |                     |  |     |  |  (Checkpoint API)   |  |
|  +----------+----------+  |     |  +---------------------+  |
|             |             |     |                           |
+-------------+-------------+     +---------------------------+
              |
              v
+---------------------------+
|                           |
|  etcd                     |
|                           |
|  +---------------------+  |
|  |                     |  |
|  |  ContainerCheckpoint|  |
|  |  CRs                |  |
|  |                     |  |
|  +---------------------+  |
|                           |
|  +---------------------+  |
|  |                     |  |
|  |  ContainerCheckpoint|  |
|  |  Content CRs        |  |
|  |                     |  |
|  +---------------------+  |
|                           |
+---------------------------+
```

## Component Descriptions

### Controller Manager
- Hosts and manages the Checkpoint Controller
- Provides metrics endpoints for monitoring
- Handles health probes for liveness and readiness
- Manages leader election for high availability
- Coordinates all controller activities

### Checkpoint Controller
- Runs within the Controller Manager
- Watches for ContainerCheckpoint CRs
- Creates ContainerCheckpointContent CRs
- Manages the checkpoint lifecycle

### Checkpoint Sidecar
- Runs as a DaemonSet on each node
- Watches for ContainerCheckpoint CRs assigned to its node
- Communicates with the local Kubelet to trigger checkpoints
- Updates ContainerCheckpoint status

### Custom Resources
1. **ContainerCheckpoint**
   - Represents a request to checkpoint a specific container
   - Contains pod, container, and node information
   - Status reflects the checkpoint operation state

2. **ContainerCheckpointContent**
   - Represents the actual checkpoint data
   - References the parent ContainerCheckpoint
   - Contains the path to the checkpoint file

### Workflow
1. User creates a ContainerCheckpoint CR
2. Checkpoint Controller (within Controller Manager) processes the CR and creates a ContainerCheckpointContent CR
3. Checkpoint Sidecar on the target node detects the CR
4. Sidecar communicates with the local Kubelet to create the checkpoint
5. Status is updated on the ContainerCheckpoint CR

This architecture enables container checkpointing across a Kubernetes cluster while maintaining proper separation of concerns between the control plane and node components.
