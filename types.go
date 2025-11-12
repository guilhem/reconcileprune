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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ManagedChild represents a single managed child resource.
type ManagedChild struct {
	// ObjectReference identifies the child resource.
	ObjectReference corev1.ObjectReference `json:"objectReference"`

	// ObservedGeneration is the parent's metadata.generation when this child was last applied.
	// Used to determine which resources should be pruned on the next reconciliation.
	ObservedGeneration int64 `json:"observedGeneration"`
}

// Result contains information about what was pruned during reconciliation.
type Result struct {
	// Pruned is the list of resources that were successfully deleted.
	Pruned []string `json:"pruned,omitempty"`

	// Skipped is the list of resources that were skipped (e.g., in dry-run mode).
	Skipped []string `json:"skipped,omitempty"`
}

// ErrorHandlerFunc is called when an error occurs during pruning operations.
// It receives the context, the error, and the object being processed.
// Return nil to ignore the error, or return/wrap the error to fail the operation.
type ErrorHandlerFunc func(ctx context.Context, err error, obj client.Object) error
