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

package reconcileprune

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCR is a minimal Custom Resource for testing
type TestCR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TestCRSpec   `json:"spec,omitempty"`
	Status            TestCRStatus `json:"status,omitempty"`
}

func (t *TestCR) DeepCopyObject() runtime.Object {
	return t.DeepCopy()
}

func (t *TestCR) DeepCopy() *TestCR {
	if t == nil {
		return nil
	}
	out := new(TestCR)
	t.DeepCopyInto(out)
	return out
}

func (t *TestCR) DeepCopyInto(out *TestCR) {
	*out = *t
	out.TypeMeta = t.TypeMeta
	t.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = t.Spec
	if t.Status.Children != nil {
		out.Status.Children = make([]ManagedChild, len(t.Status.Children))
		copy(out.Status.Children, t.Status.Children)
	}
}

type TestCRSpec struct{}

type TestCRStatus struct {
	Children []ManagedChild `json:"children,omitempty"`
}

func setupScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	scheme.AddKnownTypes(metav1.SchemeGroupVersion, &TestCR{})
	return scheme
}

func TestPruner_FirstReconcile(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			UID:       "test-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// User applies the deployment themselves
	if err := cl.Create(context.Background(), deployment); err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}

	// Mark it as reconciled
	if err := pruner.MarkReconciled(deployment); err != nil {
		t.Fatalf("MarkReconciled failed: %v", err)
	}

	// Prune and finalize
	result, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}

	// Update status
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	if len(result.Pruned) != 0 {
		t.Errorf("Expected 0 pruned resources on first reconcile, got %d", len(result.Pruned))
	}

	if len(owner.Status.Children) != 1 {
		t.Errorf("Expected 1 child in status, got %d", len(owner.Status.Children))
	}
}

func TestPruner_SecondReconcileWithPrune(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	deployment1 := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment-1",
			Namespace: "default",
			UID:       "test-deployment-1-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test1"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test1"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	deployment2 := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment-2",
			Namespace: "default",
			UID:       "test-deployment-2-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test2"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test2"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// First reconcile - deploy both
	if err := cl.Create(context.Background(), deployment1); err != nil {
		t.Fatalf("Failed to create deployment1: %v", err)
	}
	if err := pruner.MarkReconciled(deployment1); err != nil {
		t.Fatalf("First MarkReconciled deployment1 failed: %v", err)
	}

	if err := cl.Create(context.Background(), deployment2); err != nil {
		t.Fatalf("Failed to create deployment2: %v", err)
	}
	if err := pruner.MarkReconciled(deployment2); err != nil {
		t.Fatalf("First MarkReconciled deployment2 failed: %v", err)
	}

	_, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("First Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status after first reconcile: %v", err)
	}

	// Second reconcile - only deploy deployment2
	owner.SetGeneration(2)
	pruner2 := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// User only reconciles deployment2 (deployment1 should be pruned)
	if err := pruner2.MarkReconciled(deployment2); err != nil {
		t.Fatalf("Second MarkReconciled deployment2 failed: %v", err)
	}

	result, err := pruner2.Prune(context.Background())
	if err != nil {
		t.Fatalf("Second Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	if len(result.Pruned) != 1 {
		t.Errorf("Expected 1 pruned resource, got %d", len(result.Pruned))
	}

	if len(owner.Status.Children) != 1 {
		t.Errorf("Expected 1 child in status after prune, got %d", len(owner.Status.Children))
	}

	// Verify deployment-1 was deleted
	dep1 := &appsv1.Deployment{}
	err = cl.Get(context.Background(), client.ObjectKey{
		Namespace: "default",
		Name:      "test-deployment-1",
	}, dep1)
	if err == nil {
		t.Errorf("Expected deployment-1 to be deleted")
	}
}

func TestPruner_IdempotentReconcile(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			UID:       "test-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// First reconcile
	if err := cl.Create(context.Background(), deployment); err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}
	if err := pruner.MarkReconciled(deployment); err != nil {
		t.Fatalf("First MarkReconciled failed: %v", err)
	}

	_, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("First Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	// Second reconcile (same generation)
	pruner2 := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)
	if err := pruner2.MarkReconciled(deployment); err != nil {
		t.Fatalf("Second MarkReconciled failed: %v", err)
	}

	result2, err := pruner2.Prune(context.Background())
	if err != nil {
		t.Fatalf("Second Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	if len(result2.Pruned) != 0 {
		t.Errorf("Expected 0 pruned resources on idempotent reconcile, got %d", len(result2.Pruned))
	}
}

func TestPruner_DryRun(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			UID:       "test-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	prunerNormal := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// First reconcile - create deployment
	if err := cl.Create(context.Background(), deployment); err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}
	if err := prunerNormal.MarkReconciled(deployment); err != nil {
		t.Fatalf("MarkReconciled failed: %v", err)
	}

	_, err := prunerNormal.Prune(context.Background())
	if err != nil {
		t.Fatalf("Failed to prune: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	owner.SetGeneration(2)
	prunerDryRun := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
		WithDryRun(true),
	)

	// Second reconcile with dry-run (no deployment desired)
	result, err := prunerDryRun.Prune(context.Background())
	if err != nil {
		t.Fatalf("Dry-run Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	if len(result.Skipped) != 1 {
		t.Errorf("Expected 1 skipped resource in dry-run, got %d", len(result.Skipped))
	}

	if len(result.Pruned) != 0 {
		t.Errorf("Expected 0 pruned resources in dry-run, got %d", len(result.Pruned))
	}

	// Verify deployment still exists
	dep := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), client.ObjectKey{
		Namespace: "default",
		Name:      "test-deployment",
	}, dep); err != nil {
		t.Errorf("Deployment should still exist in dry-run mode: %v", err)
	}
}

func TestPruner_CustomErrorHandler(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	errorHandlerCalled := false
	errorHandler := func(ctx context.Context, err error, obj client.Object) error {
		errorHandlerCalled = true
		// Ignore error and continue
		return nil
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
		WithErrorHandler(errorHandler),
	)

	// Just run prune with no children - this won't trigger error handler
	// but verifies the option is accepted
	_, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	// Note: In a real scenario, the error handler would be called when deletion fails
	_ = errorHandlerCalled
}

func TestPruner_ErrorHandlerReturnsError(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			UID:       "test-deployment-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	// First reconcile - create deployment
	if err := cl.Create(context.Background(), deployment); err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)
	if err := pruner.MarkReconciled(deployment); err != nil {
		t.Fatalf("MarkReconciled failed: %v", err)
	}
	_, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("First Prune failed: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	// Second reconcile with error handler that returns error
	owner.SetGeneration(2)
	errorHandler := func(ctx context.Context, err error, obj client.Object) error {
		// Return the error instead of ignoring it
		return err
	}

	pruner2 := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
		WithErrorHandler(errorHandler),
	)

	// Delete the deployment manually to simulate a scenario where it doesn't exist
	// This will NOT trigger the error handler since NotFound is ignored
	_ = cl.Delete(context.Background(), deployment)

	result, err := pruner2.Prune(context.Background())

	// Should succeed since NotFound is ignored
	if err != nil {
		t.Fatalf("Prune should succeed when resource already deleted: %v", err)
	}
	if err := cl.Status().Update(context.Background(), owner); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	if len(result.Pruned) != 1 {
		t.Errorf("Expected 1 pruned resource, got %d", len(result.Pruned))
	}
}

func TestPruner_MarkReconciledWithoutUID(t *testing.T) {
	scheme := setupScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&TestCR{}).
		Build()

	owner := &TestCR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "TestCR",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-owner",
			Namespace:  "default",
			UID:        "test-uid",
			Generation: 1,
		},
	}

	if err := cl.Create(context.Background(), owner); err != nil {
		t.Fatalf("Failed to create owner: %v", err)
	}

	pruner := NewPruner(cl, owner, &owner.Status.Children,
		WithScheme(scheme),
	)

	// Try to mark an object without UID (not created yet)
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			// No UID - not created yet
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
				},
			},
		},
	}

	err := pruner.MarkReconciled(deployment)
	if err == nil {
		t.Fatalf("Expected error when marking object without UID, got nil")
	}

	expectedErrMsg := "must have a UID"
	if !contains(err.Error(), expectedErrMsg) {
		t.Errorf("Expected error to contain '%s', got: %v", expectedErrMsg, err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
