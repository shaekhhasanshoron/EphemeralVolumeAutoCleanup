/*
Copyright 2025.

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

package controller

import (
	cleanupv1 "EphemeralVolumeAutoCleanup/api/v1"
	"context"
	"fmt"
	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

// EphemeralVolumePolicyReconciler reconciles a EphemeralVolumePolicy object
type EphemeralVolumePolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	logger logr.Logger
	ctx    context.Context
}

const (
	PodCleanedLabelKey        = "ephemeral.cleanup/cleaned"
	PodMangedByLabelKey       = "app.kubernetes.io/managed-by"
	CleanupJobTTL       int32 = 15
)

// +kubebuilder:rbac:groups=cleanup.makro.com,resources=ephemeralvolumepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cleanup.makro.com,resources=ephemeralvolumepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cleanup.makro.com,resources=ephemeralvolumepolicies/finalizers,verbs=update

func (r *EphemeralVolumePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.logger = log.FromContext(ctx)
	r.ctx = ctx

	policyCr := cleanupv1.EphemeralVolumePolicy{}
	err := r.Get(r.ctx, req.NamespacedName, &policyCr)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	targetNamespaces := r.getTargetNamespaceNames(policyCr)
	for _, targetNamespace := range targetNamespaces {
		var podList corev1.PodList
		if err = r.List(ctx, &podList, client.InNamespace(targetNamespace)); err != nil {
			r.logger.Error(err, "Failed to get pod list!", "namespace", targetNamespace)
			continue
		}

		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				if pod.Labels != nil {
					if _, exists := pod.Labels[PodCleanedLabelKey]; exists {
						delete(pod.Labels, PodCleanedLabelKey)
						_ = r.Update(ctx, &pod)
						r.logger.Info("Removed clean label from pod", "pod", pod.Name, "namespace", targetNamespace)
					}
				}
				continue
			}

			if !containsEmptyDir(pod) ||
				!shouldTriggerCleanup(&policyCr, &pod) ||
				!isValidPod(pod) {
				continue
			}

			r.logger.Info("Triggering cleanup job for pod", "pod", pod.Name, "namespace", targetNamespace)
			if err = r.createCleanupJob(policyCr, &pod, CleanupJobTTL); err != nil {
				r.logger.Error(err, "Failed to trigger cleanup for job", "pod", pod.Name, "namespace", targetNamespace)
				continue
			}

			r.logger.Info("Adding clean up label to pod", "pod", pod.Name, "namespace", targetNamespace)
			if pod.Labels == nil {
				pod.Labels = map[string]string{}
			}
			pod.Labels[PodCleanedLabelKey] = "true"
			err = r.Update(ctx, &pod)
			if err != nil {
				r.logger.Error(err, "Failed to add label to pod", "pod", pod.Name, "namespace", targetNamespace)
				return ctrl.Result{}, err
			}

			policyCr.Status.LastSynced = metav1.NewTime(time.Now())
			_ = r.Status().Update(ctx, &policyCr)
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *EphemeralVolumePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cleanupv1.EphemeralVolumePolicy{}).
		Complete(r)
}

func (r *EphemeralVolumePolicyReconciler) getTargetNamespaceNames(cr cleanupv1.EphemeralVolumePolicy) []string {
	var namespaces []string
	if len(cr.Spec.TargetNamespaces) == 1 && cr.Spec.TargetNamespaces[0] == "*" {
		var nsList corev1.NamespaceList
		if err := r.List(r.ctx, &nsList); err != nil {
			return nil
		}

		for _, ns := range nsList.Items {
			namespaces = append(namespaces, ns.Name)
		}
	} else {
		namespaces = cr.Spec.TargetNamespaces
	}

	return namespaces
}

func (r *EphemeralVolumePolicyReconciler) createCleanupJob(policy cleanupv1.EphemeralVolumePolicy, pod *corev1.Pod, ttl int32) error {
	var existing batchv1.Job
	err := r.Get(r.ctx, types.NamespacedName{
		Name:      fmt.Sprintf("volume-cleanup-%s", pod.Name),
		Namespace: pod.Namespace,
	}, &existing)

	if err == nil {
		r.logger.Info("Clean up job already exists", "pod", pod.Name, "namespace", pod.Namespace)
		return nil
	} else if !k8errors.IsNotFound(err) {
		return err
	}

	r.logger.Info("Initiating clean up job", "pod", pod.Name, "namespace", pod.Namespace)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("volume-cleanup-%s", pod.UID),
			Namespace: pod.Namespace,
			Labels: map[string]string{
				PodMangedByLabelKey:                    "ephemeral-volume-controller",
				"ephemeral.makro.com/policy-owner":     policy.Name,
				"ephemeral.makro.com/policy-namespace": policy.Namespace,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"ephemeral.makro.com/job-type":           "ephemeral-cleaner",
						"ephemeral.makro.com/cleanup-target-pod": pod.Name,
						PodMangedByLabelKey:                      "ephemeral-volume-controller",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					NodeName:      pod.Spec.NodeName,
					Containers: []corev1.Container{
						{
							Name:    "cleaner",
							Image:   "busybox",
							Command: []string{"sh", "-c", fmt.Sprintf("echo 'Cleaning pod volume from host path /var/lib/kubelet/pods/%s/volumes/kubernetes.io~empty-dir/...'; rm -rf /host/pods/%s/volumes/kubernetes.io~empty-dir/*", pod.UID, pod.UID)},
							VolumeMounts: []corev1.VolumeMount{{
								Name:      "host-pods",
								MountPath: "/host/pods",
							}},
						},
					},
					Volumes: []corev1.Volume{{
						Name: "host-pods",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/var/lib/kubelet/pods",
							},
						},
					}},
				},
			},
		},
	}

	return r.Create(r.ctx, job)
}

func containsEmptyDir(pod corev1.Pod) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil {
			return true
		}
	}
	return false
}

func isValidPod(pod corev1.Pod) bool {
	if pod.Labels != nil {
		if pod.Labels[PodCleanedLabelKey] == "true" || pod.Labels[PodMangedByLabelKey] == "ephemeral-volume-controller" {
			return false
		}
	}
	return true
}

func shouldTriggerCleanup(policyCr *cleanupv1.EphemeralVolumePolicy, pod *corev1.Pod) bool {
	triggerPolicy := policyCr.Spec.CleanupPolicy
	if triggerPolicy == "OnPodCompletion" {
		return pod.Status.Phase == corev1.PodSucceeded
	} else if triggerPolicy == "OnPodFailure" {
		return pod.Status.Phase == corev1.PodFailed
	} else if triggerPolicy == "Always" {
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	} else {
		return false
	}
}
