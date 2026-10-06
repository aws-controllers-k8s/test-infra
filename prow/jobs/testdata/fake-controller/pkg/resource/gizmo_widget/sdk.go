package gizmo_widget

// Testdata, never compiled. Snake_case directory, as ACK generates for multi-word
// resources (e.g. pkg/resource/dhcp_options).

func sdkCreate() {
	input := &svcsdk.CreateGizmoWidgetInput{}
	_, _ = rm.sdkapi.CreateGizmoWidget(ctx, input)
}
