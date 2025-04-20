### Container‑Checkpoint System — **Quick Start**

> Builds everything **locally**, loads the images into CRI‑O, installs CRDs, the controller, and the sidecar, then shows one end‑to‑end checkpoint / restore flow.

---

## 0 · Prerequisites
* Go 1.22+, podman/buildah, kubectl, crictl, a working Kubernetes cluster that uses **CRI‑O**.
* Repo cloned at `$GOPATH/src/github.com/zacchaeuschok/pod-checkpoint-controller`.

---

## 1 · Build binaries

```bash
# from repo root
go build -o bin/external-checkpointer     ./cmd/main.go
go build -o bin/sidecar                  ./cmd/sidecar/main.go
```

---

## 2 · Build and load images into CRI‑O

```bash
# Controller
sudo buildah bud -t controller:latest .

# Sidecar
sudo buildah bud -t external-checkpointer-sidecar:latest -f Dockerfile.sidecar .

# Push directly into CRI‑O’s local store
sudo buildah push controller:latest \
      oci:/var/lib/containers/storage:controller:latest
sudo buildah push external-checkpointer-sidecar:latest \
      oci:/var/lib/containers/storage:external-checkpointer-sidecar:latest
```

Verify:

```bash
sudo crictl images | grep -E 'controller|external-checkpointer-sidecar'
```

---

## 3 · Install CRDs

```bash
make install   # or:
# kubectl apply -f config/crd/bases/
```

---

## 4 · Deploy controller

```bash
# RBAC & deployment
kubectl apply -k config/default   # contains rbac/, manager.yaml, etc.

# Check it came up
kubectl rollout status -n external-checkpointer-system \
        deployment/external-checkpointer-controller-manager

# OPTIONAL: restart
kubectl -n external-checkpointer-system rollout restart deployment external-checkpointer-controller-manager

```

---

## 5 · Deploy sidecar DaemonSet

```bash
kubectl apply -f config/sidecar/rbac.yaml
kubectl apply -f config/sidecar/service_account.yaml
kubectl apply -f config/sidecar/sidecar_daemonset.yaml

kubectl rollout status -n kube-system \
        ds/container-checkpoint-sidecar
```

---

## 6 · Sample workload with **two containers**

```yaml
# samples/nginx-sample.yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginx-sample
  namespace: default
spec:
  nodeSelector:
    kubernetes.io/hostname: node1        # pin for clarity
  containers:
  - name: nginx
    image: busybox
    command: ["sh","-c","i=0; while true; do echo nginx:$i; i=$((i+1)); sleep 10; done"]
  - name: sidekick
    image: busybox
    command: ["sh","-c","j=0; while true; do echo sidekick:$j; j=$((j+1)); sleep 10; done"]
```

```bash
kubectl apply -f config/samples/sample-pod.yaml
kubectl wait config/samples/sample-pod.yaml --for=condition=Ready --timeout=120s

# nginx container
kubectl logs nginx-sample -c nginx

# sidekick container
kubectl logs nginx-sample -c sidekick
```

---

## 7 · Create a **PodCheckpoint**

```yaml
# samples/pod-checkpoint.yaml
apiVersion: checkpointing.zacchaeuschok.github.io/v1
kind: PodCheckpoint
metadata:
  name: nginx-pod-checkpoint
  namespace: default
spec:
  podName: nginx-sample
  retainAfterRestore: true
```

```bash
kubectl apply -f config/samples/checkpointing_v1_podcheckpoint.yaml
```

Watch until ready:

```bash
kubectl get podcheckpoint nginx-pod-checkpoint -o wide
```

When `status.readyToRestore=true` the matching `ContainerCheckpointContent`
objects should also be ready:

```bash
kubectl get containercheckpointcontents
```

---

## 8 · Restore on another node

```bash
# Delete original
kubectl delete pod nginx-sample

# New pod (node2) annotated to restore
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: restored-nginx
  namespace: default
  annotations:
    checkpointing.zacchaeuschok.github.io/restore-from: nginx-pod-checkpoint
spec:
  nodeSelector:
    kubernetes.io/hostname: node2
  containers:
  - name: nginx
    image: busybox
    command: ["sh","-c","echo restored nginx; sleep 3600"]
  - name: sidekick
    image: busybox
    command: ["sh","-c","echo restored sidekick; sleep 3600"]
EOF
```

Verify it lands on node 2 and resumes.

---

## 9 · Troubleshooting quick refs

```bash
# Controller logs
kubectl logs -n external-checkpointer-system -l control-plane=controller-manager --tail=100

# Sidecar logs
kubectl logs -n kube-system -l app=container-checkpoint-sidecar --tail=100

# Image present?
sudo crictl images | grep controller
sudo crictl images | grep external-checkpointer-sidecar

# Kubelet checkpoint endpoint (run on the node)
curl -k --unix-socket /var/run/crio/crio.sock \
     -X POST https://localhost:10250/checkpoint/default/nginx-sample/nginx
```

---

## 10 · Clean up

```bash
kubectl delete -f samples/
kubectl delete -f samples/pod-checkpoint.yaml
kubectl delete -f config/sidecar/                       # RBAC + DS
kubectl delete -k config/default/
make uninstall   # removes CRDs
```

---

*This streamlined guide is minimal but complete for local CRI‑O clusters. Adjust image names, node selectors, and storage paths to suit your environment.*