package widget

// Testdata mimicking generated ACK resource manager code; never compiled.

func sdkCreate() {
	input := &svcsdk.CreateWidgetInput{}
	resp, err := rm.sdkapi.CreateWidget(ctx, input)
	_, _ = resp, err
}

func sdkDelete() {
	input := &svcsdk.DeleteWidgetInput{}
	_, _ = rm.sdkapi.DeleteWidget(ctx, input)
}
