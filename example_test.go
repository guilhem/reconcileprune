// Copyright 2025 The Kubernetes Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reconcileprune_test

import (
	"context"
	"fmt"

	"github.com/guilhem/reconcileprune"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// MyCR represents a simple Custom Resource
type MyCR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MyCRSpec   `json:"spec,omitempty"`
	Status            MyCRStatus `json:"status,omitempty"`
}

func (m *MyCR) DeepCopyObject() runtime.Object {
	return m.DeepCopy()
}

func (m *MyCR) DeepCopy() *MyCR {
	if m == nil {
		return nil
	}
	out := new(MyCR)
	m.DeepCopyInto(out)
	return out
}

func (m *MyCR) DeepCopyInto(out *MyCR) {
	*out = *m
	out.TypeMeta = m.TypeMeta
	m.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = m.Spec
	if m.Status.Children != nil {
		out.Status.Children = make([]reconcileprune.ManagedChild, len(m.Status.Children))
		copy(out.Status.Children, m.Status.Children)
	}
}

type MyCRSpec struct {
	Replicas int32 `json:"replicas"`
}

type MyCRStatus struct {
	Children []reconcileprune.ManagedChild `json:"children,omitempty"`
}

func ExamplePruner_MarkReconciled() {
	// Setup
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	scheme.AddKnownTypes(metav1.SchemeGroupVersion, &MyCR{})

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&MyCR{}).
		Build()

	owner := &MyCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "MyCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-cr",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
		Spec: MyCRSpec{
			Replicas: 3,
		},
	}

	_ = cl.Create(context.Background(), owner)

	// Create pruner
	pruner := reconcileprune.NewPruner(cl, owner, &owner.Status.Children,
		reconcileprune.WithScheme(scheme),
	)

	// User applies resources themselves
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deployment",
			Namespace: "default",
			UID:       "my-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &owner.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "my-app"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "my-app"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				},
			},
		},
	}

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-service",
			Namespace: "default",
			UID:       "my-service-uid",
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "my-app"},
			Ports: []corev1.ServicePort{
				{Port: 80},
			},
		},
	}

	// Apply resources using your preferred method
	_ = cl.Create(context.Background(), deployment)
	_ = cl.Create(context.Background(), service)

	// Mark them as reconciled
	_ = pruner.MarkReconciled(deployment)
	_ = pruner.MarkReconciled(service)

	// Prune stale resources
	result, err := pruner.Prune(context.Background())
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Update status to persist changes
	_ = cl.Status().Update(context.Background(), owner)

	fmt.Printf("Pruned: %d resources\n", len(result))
	fmt.Printf("Children tracked: %d\n", len(owner.Status.Children))

	// Output:
	// Pruned: 0 resources
	// Children tracked: 2
}

func ExamplePruner_MarkReconciled_withDryRun() {
	// Setup
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	scheme.AddKnownTypes(metav1.SchemeGroupVersion, &MyCR{})

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&MyCR{}).
		Build()

	owner := &MyCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "MyCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-cr",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
		Spec: MyCRSpec{
			Replicas: 3,
		},
	}

	_ = cl.Create(context.Background(), owner)

	// First reconcile - create a deployment
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deployment",
			Namespace: "default",
			UID:       "my-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &owner.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "my-app"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "my-app"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				},
			},
		},
	}

	_ = cl.Create(context.Background(), deployment)

	pruner1 := reconcileprune.NewPruner(cl, owner, &owner.Status.Children,
		reconcileprune.WithScheme(scheme),
	)
	_ = pruner1.MarkReconciled(deployment)
	_, _ = pruner1.Prune(context.Background())
	_ = cl.Status().Update(context.Background(), owner)

	// Second reconcile with dry-run - don't reconcile deployment (it should be pruned)
	owner.SetGeneration(2)
	pruner2 := reconcileprune.NewPruner(cl, owner, &owner.Status.Children,
		reconcileprune.WithScheme(scheme),
		reconcileprune.WithDryRun(true),
	)

	pruned, _ := pruner2.Prune(context.Background())
	_ = cl.Status().Update(context.Background(), owner)

	fmt.Printf("Pruned (dry-run): %d resources\n", len(pruned))

	// Output:
	// Pruned (dry-run): 1 resources
}

func ExamplePruner_MarkReconciled_customErrorHandler() {
	// Setup
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	scheme.AddKnownTypes(metav1.SchemeGroupVersion, &MyCR{})

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&MyCR{}).
		Build()

	owner := &MyCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "MyCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-cr",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
		Spec: MyCRSpec{
			Replicas: 3,
		},
	}

	_ = cl.Create(context.Background(), owner)

	// Custom error handler
	errorHandler := func(ctx context.Context, err error, obj client.Object) error {
		// Log the error but continue (return nil to ignore)
		fmt.Printf("Warning: failed to delete %s/%s: %v\n",
			obj.GetNamespace(), obj.GetName(), err)
		return nil // Ignore error and continue
	}

	pruner := reconcileprune.NewPruner(cl, owner, &owner.Status.Children,
		reconcileprune.WithScheme(scheme),
		reconcileprune.WithErrorHandler(errorHandler),
	)

	// Example reconciliation...
	_, _ = pruner.Prune(context.Background())
	_ = cl.Status().Update(context.Background(), owner)

	fmt.Println("Reconciliation completed")

	// Output:
	// Reconciliation completed
}
