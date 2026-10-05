package gizmo_widget

// Testdata only, never compiled. This file exists to prove the scan skips
// _test.go: DeleteGizmoWidgetMockOnly is referenced here and nowhere else, so it
// must NOT appear in the used-operation set.

func TestSomething() {
	_ = &svcsdk.DeleteGizmoWidgetMockOnlyInput{}
}
