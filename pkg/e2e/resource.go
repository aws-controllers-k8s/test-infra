package e2e

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultDeleteTimeout = 120 * time.Second
	deletePollInterval   = 5 * time.Second
)

// CreateResource creates a K8s custom resource and waits for the controller to
// pick it up (i.e., adds a finalizer or sets a condition).
func CreateResource(ctx context.Context, obj client.Object) error {
	return Client().Create(ctx, obj)
}

// UpdateResource updates the spec of an existing K8s custom resource.
func UpdateResource(ctx context.Context, obj client.Object) error {
	return Client().Update(ctx, obj)
}

// PatchResource applies a merge patch to a K8s custom resource.
func PatchResource(ctx context.Context, obj client.Object, patch client.Patch) error {
	return Client().Patch(ctx, obj, patch)
}

// DeleteResource deletes a K8s custom resource and waits for it to be fully
// removed (finalizer processed, object gone).
func DeleteResource(ctx context.Context, obj client.Object) error {
	err := Client().Delete(ctx, obj)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting resource: %w", err)
	}

	// Wait for the resource to be fully removed
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	return wait.PollUntilContextTimeout(ctx, deletePollInterval, defaultDeleteTimeout, true, func(ctx context.Context) (bool, error) {
		err := Client().Get(ctx, key, obj)
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}

// GetResource fetches the current state of a K8s custom resource.
func GetResource(ctx context.Context, obj client.Object) error {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	return Client().Get(ctx, key, obj)
}
