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
	"context"
	"fmt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	cleanupv1 "EphemeralVolumeAutoCleanup/api/v1"
)

var _ = Describe("EphemeralVolumePolicy Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-policy"
			namespace    = "default"
			podName      = "test-pod"
		)

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}
		ephemeralvolumepolicy := &cleanupv1.EphemeralVolumePolicy{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind EphemeralVolumePolicy")
			err := k8sClient.Get(ctx, typeNamespacedName, ephemeralvolumepolicy)
			if err != nil && errors.IsNotFound(err) {
				resource := &cleanupv1.EphemeralVolumePolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: cleanupv1.EphemeralVolumePolicySpec{
						CleanupPolicy:    "OnPodFailure",
						TargetNamespaces: []string{namespace},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}

			By("creating a failed pod with emptyDir volume")
			testPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: namespace,
					UID:       "random-uid-123",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name:         "ephemeral",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "test-container",
						Image:   "busybox",
						Command: []string{"sh", "-c", "echo test > /data/file.txt"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "ephemeral",
							MountPath: "/data",
						}},
					}},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodFailed,
				},
			}
			Expect(k8sClient.Create(ctx, testPod)).To(Succeed())

			testPod.Status.Phase = corev1.PodFailed
			Expect(k8sClient.Status().Update(ctx, testPod)).To(Succeed())
		})

		AfterEach(func() {
			By("Deleting the test pod and policy cr")
			Expect(k8sClient.Delete(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: namespace,
				},
			})).To(Succeed())

			Expect(k8sClient.Delete(ctx, &cleanupv1.EphemeralVolumePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
			})).To(Succeed())
		})
		It("should reconcile and label the pod and create cleanup job", func() {
			reconciler := &EphemeralVolumePolicyReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			pod := &corev1.Pod{}
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, pod)
			fmt.Printf("=== Pre-EVENTUALLY ===\n")
			fmt.Printf("Pod.Status.Phase: %v\n", pod.Status.Phase)
			fmt.Printf("Pod.Labels: %#v\n", pod.Labels)
			fmt.Printf("Pod.Volumes: %#v\n", pod.Spec.Volumes)
			fmt.Printf("containsEmptyDir: %v\n", containsEmptyDir(*pod))
			// also fetch and print policy
			policy := &cleanupv1.EphemeralVolumePolicy{}
			_ = k8sClient.Get(ctx, typeNamespacedName, policy)
			fmt.Printf("Policy.CleanupPolicy: %s\n", policy.Spec.CleanupPolicy)
			fmt.Printf("shouldTriggerCleanup: %v\n", shouldTriggerCleanup(policy, pod))

			By("checking if the pod has the cleaned label")
			Eventually(func(g Gomega) {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, pod)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(pod.Labels).To(HaveKeyWithValue(PodCleanedLabelKey, "true"))
			}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())

			By("checking if a cleanup job was created")
			Eventually(func(g Gomega) {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(jobList.Items).NotTo(BeEmpty())
			}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())

			By("checking CR status was updated")
			Eventually(func(g Gomega) {
				policy := &cleanupv1.EphemeralVolumePolicy{}
				err := k8sClient.Get(ctx, typeNamespacedName, policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.LastSynced.Time).ToNot(BeZero())
			}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
		})
	})
})

//var _ = Describe("EphemeralVolumePolicy Reconciler", func() {
//	//var (
//	//	scheme     *runtime.Scheme
//	//	reconciler *EphemeralVolumePolicyReconciler
//	//	ctx        context.Context
//	//	podName    = "test-pod"
//	//	crName     = "test-policy"
//	//	namespace  = "default"
//	//)
//
//	BeforeEach(func() {
//		scheme = runtime.NewScheme()
//		Expect(corev1.AddToScheme(scheme)).To(Succeed())
//		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
//		Expect(cleanupv1.AddToScheme(scheme)).To(Succeed())
//
//		ctx = context.TODO()
//
//		// EphemeralVolumePolicy CR
//		cr := &cleanupv1.EphemeralVolumePolicy{
//			ObjectMeta: metav1.ObjectMeta{
//				Name:      crName,
//				Namespace: namespace,
//			},
//		}
//
//		// Pod in Failed state with emptyDir
//		pod := &corev1.Pod{
//			ObjectMeta: metav1.ObjectMeta{
//				Name:      podName,
//				Namespace: namespace,
//			},
//			Spec: corev1.PodSpec{
//				RestartPolicy: corev1.RestartPolicyNever,
//				Volumes: []corev1.Volume{
//					{
//						Name: "temp-volume",
//						VolumeSource: corev1.VolumeSource{
//							EmptyDir: &corev1.EmptyDirVolumeSource{},
//						},
//					},
//				},
//				Containers: []corev1.Container{
//					{Name: "busybox", Image: "busybox"},
//				},
//			},
//			Status: corev1.PodStatus{
//				Phase: corev1.PodFailed,
//			},
//		}
//
//		k8sClient = fake.NewClientBuilder().
//			WithScheme(scheme).
//			WithObjects(cr, pod).
//			Build()
//
//		reconciler = &EphemeralVolumePolicyReconciler{
//			Client: k8sClient,
//			Scheme: scheme,
//		}
//	})
//
//	It("should label pod, create cleanup job, and update CR status", func() {
//		_, err := reconciler.Reconcile(ctx, reconcile.Request{
//			NamespacedName: types.NamespacedName{
//				Name:      crName,
//				Namespace: namespace,
//			},
//		})
//		Expect(err).ToNot(HaveOccurred())
//
//		By("checking if the pod has the cleaned label")
//		Eventually(func(g Gomega) {
//			pod := &corev1.Pod{}
//			err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, pod)
//			g.Expect(err).ToNot(HaveOccurred())
//			g.Expect(pod.Labels).To(HaveKeyWithValue("ephemeral.cleanup/cleaned", "true"))
//		}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
//
//		By("checking if a cleanup job was created")
//		Eventually(func(g Gomega) {
//			jobList := &batchv1.JobList{}
//			err := k8sClient.List(ctx, jobList)
//			g.Expect(err).ToNot(HaveOccurred())
//			g.Expect(jobList.Items).ToNot(BeEmpty())
//		}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
//
//		By("checking CR status was updated")
//		Eventually(func(g Gomega) {
//			cr := &cleanupv1.EphemeralVolumePolicy{}
//			err := k8sClient.Get(ctx, types.NamespacedName{Name: crName, Namespace: namespace}, cr)
//			g.Expect(err).ToNot(HaveOccurred())
//			g.Expect(cr.Status.LastSynced.Time).ToNot(BeZero())
//		}).WithTimeout(5 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
//	})
//})
