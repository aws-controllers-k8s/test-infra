package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultPollInterval = 5 * time.Second
)

// WaitForCondition polls the resource until the named condition has the expected
// status value, or until the timeout elapses.
func WaitForCondition(
	ctx context.Context,
	obj client.Object,
	conditionType string,
	expectedStatus string,
	timeout time.Duration,
) error {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	return wait.PollUntilContextTimeout(ctx, defaultPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := Client().Get(ctx, key, obj); err != nil {
			return false, nil
		}
		status, err := getConditionStatus(obj, conditionType)
		if err != nil {
			return false, nil
		}
		return status == expectedStatus, nil
	})
}

// WaitForDeletion waits until the resource no longer exists in the cluster.
func WaitForDeletion(ctx context.Context, obj client.Object, timeout time.Duration) error {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	return wait.PollUntilContextTimeout(ctx, defaultPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		err := Client().Get(ctx, key, obj)
		if err != nil {
			return true, nil
		}
		return false, nil
	})
}

// AssertCondition returns an error if the resource does not currently have the
// specified condition with the expected status.
func AssertCondition(ctx context.Context, obj client.Object, conditionType, expectedStatus string) error {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	if err := Client().Get(ctx, key, obj); err != nil {
		return fmt.Errorf("getting resource: %w", err)
	}
	status, err := getConditionStatus(obj, conditionType)
	if err != nil {
		return err
	}
	if status != expectedStatus {
		return fmt.Errorf("condition %s has status %q, expected %q", conditionType, status, expectedStatus)
	}
	return nil
}

func getConditionStatus(obj client.Object, conditionType string) (string, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("marshaling object: %w", err)
	}
	var u unstructured.Unstructured
	if err := json.Unmarshal(raw, &u.Object); err != nil {
		return "", fmt.Errorf("unmarshaling to unstructured: %w", err)
	}

	conditions, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return "", fmt.Errorf("conditions not found")
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cType, _ := cond["type"].(string)
		cStatus, _ := cond["status"].(string)
		if cType == conditionType {
			return cStatus, nil
		}
	}
	return "", fmt.Errorf("condition %s not found", conditionType)
}
