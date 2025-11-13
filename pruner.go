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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/reference"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Pruner manages reconciliation and pruning of child resources.
// Create a new instance for each reconciliation session using NewPruner.
type Pruner struct {
	client       client.Client
	scheme       *runtime.Scheme
	deleteOpts   []client.DeleteOption
	errorHandler ErrorHandlerFunc

	// Reconciliation state
	owner          client.Object
	children       *[]ManagedChild
	desiredRefs    map[corev1.ObjectReference]struct{}
	pruned         []corev1.ObjectReference
	lastAppliedGen int64
}

// NewPruner creates a new Pruner instance for a reconciliation session.
// Create a new instance for each reconcile loop.
//
// Parameters:
//   - c: The Kubernetes client
//   - owner: The parent Custom Resource that owns the children
//   - children: Pointer to the slice of ManagedChild in the owner's status
//   - opts: Optional configuration options
//
// Example:
//
//	pruner := reconcileprune.NewPruner(r.Client, &myCR, &myCR.Status.Children,
//	    reconcileprune.WithFieldOwner("my-controller"),
//	    reconcileprune.WithScheme(r.Scheme),
//	)
func NewPruner(c client.Client, owner client.Object, children *[]ManagedChild, opts ...Option) *Pruner {
	p := &Pruner{
		client:       c,
		errorHandler: defaultErrorHandler,
		owner:        owner,
		children:     children,
		desiredRefs:  make(map[corev1.ObjectReference]struct{}),
		pruned:       []corev1.ObjectReference{},
	}

	// Capture the last applied generation BEFORE any modifications
	currentGen := owner.GetGeneration()
	p.lastAppliedGen = getLastAppliedGeneration(*children, currentGen)

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// MarkReconciled marks an object as reconciled (desired) for this session.
// Call this after successfully applying/reconciling a child resource.
// Must be called before Prune().
//
// The user is responsible for applying the resource using their preferred method
// (e.g., SSA, Create/Update, or any other approach).
//
// Returns an error if the object reference cannot be generated or if the object
// has not been created yet (missing UID).
func (p *Pruner) MarkReconciled(obj client.Object) error {
	// Validate that the object has been created (has a UID)
	if obj.GetUID() == "" {
		return fmt.Errorf("object %s/%s must have a UID (has it been created in the cluster?)",
			obj.GetNamespace(), obj.GetName())
	}

	// Generate reference for this object
	ref, err := reference.GetReference(p.scheme, obj)
	if err != nil {
		return fmt.Errorf("failed to generate reference for object: %w", err)
	}

	// Track as desired
	p.desiredRefs[*ref] = struct{}{}

	// Update child tracking
	currentGen := p.owner.GetGeneration()
	p.upsertChild(p.children, *ref, currentGen)

	return nil
}

// Prune removes stale resources that were not marked as reconciled in this session.
// Must be called after all MarkReconciled() calls.
// This method prunes resources from previous generations that are no longer desired.
//
// The children slice is modified in-place. After Prune() returns successfully,
// you should update the owner's status subresource to persist the changes.
//
// Parameters:
//   - ctx: Context for the operation
//
// Returns:
//   - List of pruned resources as ObjectReferences
//   - error if the pruning operation fails
//
// Example:
//
//	pruned, err := pruner.Prune(ctx)
//	if err != nil {
//	    return ctrl.Result{}, err
//	}
//	// Update status to persist changes
//	if err := r.Status().Update(ctx, &myCR); err != nil {
//	    return ctrl.Result{}, err
//	}
func (p *Pruner) Prune(ctx context.Context) ([]corev1.ObjectReference, error) {
	// Get current generation
	currentGen := p.owner.GetGeneration()

	// Prune resources from previous generation that are no longer desired
	// Only prune if the spec has changed (currentGen > lastAppliedGen captured in constructor)
	if currentGen > p.lastAppliedGen {
		pruneErrors := p.pruneStaleResources(ctx, p.children, p.desiredRefs, p.lastAppliedGen)
		if len(pruneErrors) > 0 {
			return p.pruned, errors.Join(pruneErrors...)
		}
	}

	return p.pruned, nil
}

// pruneStaleResources deletes resources from previous generations that are no longer desired.
func (p *Pruner) pruneStaleResources(
	ctx context.Context,
	children *[]ManagedChild,
	desiredRefs map[corev1.ObjectReference]struct{},
	lastAppliedGen int64,
) []error {
	var pruneErrors []error
	newChildren := []ManagedChild{}

	for _, child := range *children {
		// Keep if it's in the desired set
		if _, desired := desiredRefs[child.ObjectReference]; desired {
			newChildren = append(newChildren, child)
			continue
		}

		// Keep if it's from the current generation (just applied)
		if child.ObservedGeneration > lastAppliedGen {
			newChildren = append(newChildren, child)
			continue
		}

		// This child is from a previous generation and not desired - prune it
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(child.ObjectReference.APIVersion)
		obj.SetKind(child.ObjectReference.Kind)
		obj.SetName(child.ObjectReference.Name)
		obj.SetNamespace(child.ObjectReference.Namespace)

		if err := p.deleteResource(ctx, obj); err != nil {
			// Call error handler
			handledErr := p.errorHandler(ctx, err, obj)
			if handledErr != nil {
				pruneErrors = append(pruneErrors, handledErr)
				// Keep the child in status if deletion failed
				newChildren = append(newChildren, child)
			} else {
				// Error was ignored by handler, record as pruned
				p.pruned = append(p.pruned, child.ObjectReference)
			}
		} else {
			p.pruned = append(p.pruned, child.ObjectReference)
		}
	}

	*children = newChildren
	return pruneErrors
}

// deleteResource deletes a resource, ignoring NotFound errors.
func (p *Pruner) deleteResource(ctx context.Context, obj client.Object) error {
	if err := p.client.Delete(ctx, obj, p.deleteOpts...); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return err
	}
	return nil
}

// upsertChild updates or adds a child to the children list.
func (p *Pruner) upsertChild(children *[]ManagedChild, ref corev1.ObjectReference, observedGeneration int64) {
	for i := range *children {
		if (*children)[i].ObjectReference == ref {
			(*children)[i].ObservedGeneration = observedGeneration
			return
		}
	}
	*children = append(*children, ManagedChild{
		ObjectReference:    ref,
		ObservedGeneration: observedGeneration,
	})
}

// getLastAppliedGeneration returns the maximum ObservedGeneration from children.
// If all children have the current generation, returns currentGen.
// Otherwise returns the highest generation found.
func getLastAppliedGeneration(children []ManagedChild, currentGen int64) int64 {
	if len(children) == 0 {
		return 0
	}

	maxGen := int64(0)
	for _, child := range children {
		if child.ObservedGeneration > maxGen {
			maxGen = child.ObservedGeneration
		}
	}

	// If all children are at current generation, return current
	if maxGen == currentGen {
		return currentGen
	}

	return maxGen
}
