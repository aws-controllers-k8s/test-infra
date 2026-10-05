package widget

// This fixture mimics generated ACK resource manager code. It is testdata
// only and is never compiled as part of the tool.

func sdkCreate() {
	input := &svcsdk.CreateWidgetInput{}
	resp, err := rm.sdkapi.CreateWidget(ctx, input)
	_, _ = resp, err
}

func sdkDelete() {
	input := &svcsdk.DeleteWidgetInput{}
	_, _ = rm.sdkapi.DeleteWidget(ctx, input)
}
