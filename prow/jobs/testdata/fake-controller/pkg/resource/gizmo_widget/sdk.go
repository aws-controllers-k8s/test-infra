package gizmo_widget

// Testdata only, never compiled — see the note in widget/sdk.go. The directory
// name is deliberately snake_case, matching how ACK really generates multi-word
// resource packages (pkg/resource/dhcp_options).

func sdkCreate() {
	input := &svcsdk.CreateGizmoWidgetInput{}
	_, _ = rm.sdkapi.CreateGizmoWidget(ctx, input)
}
