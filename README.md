# Ephemeral Volume Auto Cleanup

Ephemeral Volume Auto Cleanup is an operator which manages EphemeralVolumePolicy controller. The controller is responsible 
for checking pods with terminal state (e.g. pods with status Failed/Succeeded) depending on trigger policy and cleaning its 
empty directory data. The controller creates a cleanup job that deletes the data.

## Deploy Controller Manager

### Docker Build and Push

````
make docker-build docker-push
````

### Deploy to Kubernetes

Controller can be deployed using three approaches:

**Using `Makefile`:**

For deploying the controller to the K8s cluster specified in ~/.kube/config.
```
make deploy
```

**Using `kubectl`:**

Manager manifests can be directly applied,

```
kubectl apply -f kube/crd.yaml

kubectl apply -f kube/manager.yaml
```

**Using `Helm`:**

Controller can be deployed using helm chart. For instructions, [click here](https://github.com/shaekhhasanshoron/ephemeral-volume-auto-cleanup-chart?tab=readme-ov-file#ephemeral-volume-auto-cleanup-helm-chart).

## Test Controller

```
make test
```

## Examples

```
kubectl apply -f example/cleanup-cr-one.yaml
kubectl apply -f example/cleanup-cr-two.yaml

kubectl apply -f example/pod-one.yaml
kubectl apply -f example/pod-two.yaml
```

## Live Recoding

Click the link to watch the recording [https://asciinema.org/a/wPOVuJsQzBDIamwYWugvRjFNx](https://asciinema.org/a/wPOVuJsQzBDIamwYWugvRjFNx)