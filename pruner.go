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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Pruner manages reconciliation and pruning of child resources.
// Create a new instance for each reconciliation session using NewPruner.
type Pruner struct {
	client       client.Client
	scheme       *runtime.Scheme
	dryRun       bool
	errorHandler ErrorHandlerFunc

	// Reconciliation state
	owner          client.Object
	children       *[]ManagedChild
	desiredRefs    map[string]struct{}
	result         *Result
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
		desiredRefs:  make(map[string]struct{}),
		result:       &Result{},
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
	ref, err := p.makeObjectReference(obj)
	if err != nil {
		return fmt.Errorf("failed to generate reference for object: %w", err)
	}

	// Track as desired using a string key
	refKey := makeRefKey(ref)
	p.desiredRefs[refKey] = struct{}{}

	// Update child tracking
	currentGen := p.owner.GetGeneration()
	p.upsertChild(p.children, ref, currentGen)

	return nil
}

// Prune removes stale resources that were not marked as reconciled in this session.
// Must be called after all MarkReconciled() calls.
// This method prunes resources from previous generations that are no longer desired.
//
// Parameters:
//   - ctx: Context for the operation
//   - updateStatus: Callback to update the owner's status subresource
//
// Returns:
//   - Result containing lists of pruned/skipped resources
//   - error if the operation fails
func (p *Pruner) Prune(ctx context.Context, updateStatus func(context.Context) error) (*Result, error) {
	// Get current generation
	currentGen := p.owner.GetGeneration()

	// Prune resources from previous generation that are no longer desired
	// Only prune if the spec has changed (currentGen > lastAppliedGen captured in constructor)
	if currentGen > p.lastAppliedGen {
		pruneErrors := p.pruneStaleResources(ctx, p.children, p.desiredRefs, p.lastAppliedGen, p.result)
		if len(pruneErrors) > 0 {
			err := errors.Join(pruneErrors...)
			if updateErr := updateStatus(ctx); updateErr != nil {
				return p.result, fmt.Errorf("failed to update status after prune errors: %w", updateErr)
			}
			return p.result, err
		}
	}

	// Success - update status
	if err := updateStatus(ctx); err != nil {
		return p.result, fmt.Errorf("failed to update status after successful reconcile: %w", err)
	}

	return p.result, nil
}

// pruneStaleResources deletes resources from previous generations that are no longer desired.
func (p *Pruner) pruneStaleResources(
	ctx context.Context,
	children *[]ManagedChild,
	desiredRefs map[string]struct{},
	lastAppliedGen int64,
	result *Result,
) []error {
	var pruneErrors []error
	newChildren := []ManagedChild{}

	for _, child := range *children {
		// Keep if it's in the desired set
		childKey := makeRefKey(child.ObjectReference)
		if _, desired := desiredRefs[childKey]; desired {
			newChildren = append(newChildren, child)
			continue
		}

		// Keep if it's from the current generation (just applied)
		if child.ObservedGeneration > lastAppliedGen {
			newChildren = append(newChildren, child)
			continue
		}

		// This child is from a previous generation and not desired - prune it
		obj, err := p.objectFromReference(child.ObjectReference)
		if err != nil {
			refStr := formatObjectReference(child.ObjectReference)
			pruneErrors = append(pruneErrors, fmt.Errorf("failed to create object from reference %s: %w", refStr, err))
			continue
		}

		refStr := formatObjectReference(child.ObjectReference)
		if p.dryRun {
			result.Skipped = append(result.Skipped, refStr)
			continue
		}

		if err := p.deleteResource(ctx, obj); err != nil {
			// Call error handler
			handledErr := p.errorHandler(ctx, err, obj)
			if handledErr != nil {
				pruneErrors = append(pruneErrors, handledErr)
				// Keep the child in status if deletion failed
				newChildren = append(newChildren, child)
			} else {
				// Error was ignored by handler, record as pruned
				result.Pruned = append(result.Pruned, refStr)
			}
		} else {
			result.Pruned = append(result.Pruned, refStr)
		}
	}

	*children = newChildren
	return pruneErrors
}

// deleteResource deletes a resource, ignoring NotFound errors.
func (p *Pruner) deleteResource(ctx context.Context, obj client.Object) error {
	if err := p.client.Delete(ctx, obj); err != nil {
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
	// Not found, add new
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

// makeObjectReference creates a corev1.ObjectReference from a client.Object.
func (p *Pruner) makeObjectReference(obj client.Object) (corev1.ObjectReference, error) {
	gvk, err := p.getGVK(obj)
	if err != nil {
		return corev1.ObjectReference{}, err
	}

	return corev1.ObjectReference{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		UID:        obj.GetUID(),
	}, nil
}

// objectFromReference creates a client.Object from a corev1.ObjectReference.
func (p *Pruner) objectFromReference(ref corev1.ObjectReference) (client.Object, error) {
	// Parse APIVersion to get GVK
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid APIVersion in reference: %w", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    ref.Kind,
	}

	// Create an object using the scheme
	obj, err := p.scheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create object for GVK %s: %w", gvk, err)
	}

	clientObj, ok := obj.(client.Object)
	if !ok {
		return nil, fmt.Errorf("object does not implement client.Object")
	}

	clientObj.SetName(ref.Name)
	clientObj.SetNamespace(ref.Namespace)

	return clientObj, nil
}

// makeRefKey creates a unique string key for an ObjectReference.
func makeRefKey(ref corev1.ObjectReference) string {
	return fmt.Sprintf("%s/%s/%s/%s", ref.APIVersion, ref.Kind, ref.Namespace, ref.Name)
}

// formatObjectReference formats an ObjectReference as a string for display.
func formatObjectReference(ref corev1.ObjectReference) string {
	if ref.Namespace != "" {
		return fmt.Sprintf("%s %s/%s", ref.Kind, ref.Namespace, ref.Name)
	}
	return fmt.Sprintf("%s %s", ref.Kind, ref.Name)
}

// getGVK returns the GroupVersionKind for an object.
func (p *Pruner) getGVK(obj client.Object) (schema.GroupVersionKind, error) {
	if p.scheme == nil {
		return schema.GroupVersionKind{}, fmt.Errorf("scheme is required but not set")
	}

	gvks, _, err := p.scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("failed to get GVK: %w", err)
	}

	if len(gvks) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("no GVK found for object")
	}

	return gvks[0], nil
}
