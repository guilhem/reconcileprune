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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Option configures a Pruner instance.
type Option func(*Pruner)

// WithScheme sets the runtime.Scheme used for type resolution.
// This is required for proper GVK (GroupVersionKind) handling.
//
// Example:
//
//	pruner := NewPruner(client, WithScheme(scheme))
func WithScheme(scheme *runtime.Scheme) Option {
	return func(p *Pruner) {
		p.scheme = scheme
	}
}

// WithDryRun enables dry-run mode where delete operations are simulated.
// Uses Kubernetes dry-run to validate deletions without actually removing resources.
// Resources that would be pruned are returned in the Result.
//
// Example:
//
//	pruner := NewPruner(client, WithDryRun(true))
func WithDryRun(dryRun bool) Option {
	return func(p *Pruner) {
		if dryRun {
			p.deleteOpts = []client.DeleteOption{client.DryRunAll}
		}
	}
}

// WithErrorHandler sets a custom error handler for pruning operations.
// The handler is called when an error occurs during deletion.
// If the handler returns nil, the error is ignored and pruning continues.
// If it returns an error, the operation fails.
//
// Default behavior (if not set): collect all errors and fail at the end.
//
// Example:
//
//	pruner := NewPruner(client, WithErrorHandler(func(ctx context.Context, err error, obj client.Object) error {
//	    log.Error(err, "failed to delete", "object", client.ObjectKeyFromObject(obj))
//	    return nil // ignore and continue
//	}))
func WithErrorHandler(handler ErrorHandlerFunc) Option {
	return func(p *Pruner) {
		p.errorHandler = handler
	}
}

// defaultErrorHandler aggregates errors and returns them at the end.
func defaultErrorHandler(ctx context.Context, err error, obj client.Object) error {
	// Return the error to aggregate it
	return fmt.Errorf("failed to delete %s: %w", client.ObjectKeyFromObject(obj), err)
}
