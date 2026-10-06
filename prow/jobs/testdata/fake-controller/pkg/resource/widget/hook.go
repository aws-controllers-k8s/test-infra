package widget

// Testdata, never compiled; undefined symbols are intentional.

func syncTags() {
	input := &svcsdk.PutWidgetTaggingInput{}
	_, _ = rm.sdkapi.PutWidgetTagging(ctx, input)
}

func readConfig() {
	_, _ = rm.sdkapi.GetWidgetConfig(ctx, &svcsdk.GetWidgetConfigInput{})
}
