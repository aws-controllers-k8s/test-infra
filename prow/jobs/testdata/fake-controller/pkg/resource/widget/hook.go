package widget

// Testdata only, never compiled — same as widget/sdk.go. The undefined symbols
// below are intentional.

func syncTags() {
	input := &svcsdk.PutWidgetTaggingInput{}
	_, _ = rm.sdkapi.PutWidgetTagging(ctx, input)
}

func readConfig() {
	_, _ = rm.sdkapi.GetWidgetConfig(ctx, &svcsdk.GetWidgetConfigInput{})
}
