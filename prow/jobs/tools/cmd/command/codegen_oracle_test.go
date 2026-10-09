package command

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codegenFixtures = "../../../testdata/codegen_oracle/"

// codegenFixtureInputs is a controller for the fixture models with a CRD for
// Widget only.
func codegenFixtureInputs(generatorYAML string) *ControllerInputs {
	return &ControllerInputs{
		Service:             "widgets",
		ModelName:           "widgets",
		GeneratorConfigPath: generatorYAML,
		Config:              &generatorConfig{},
		CRDFields:           map[string]map[string]bool{"Widget": {"name": true}},
		CRDStatusFields:     map[string]map[string]bool{"Widget": {"state": true}},
		kindsByLower:        map[string]string{"widget": "Widget"},
	}
}

func loadCodegenFixture(t *testing.T, name string) *SmithyModel {
	t.Helper()
	data, err := os.ReadFile(codegenFixtures + name)
	require.NoError(t, err)
	m, err := LoadSmithyModel(data)
	require.NoError(t, err)
	return m
}

func pathKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRunCodegenOracle(t *testing.T) {
	in := codegenFixtureInputs(codegenFixtures + "generator.yaml")
	view, err := runCodegenOracle(in, loadCodegenFixture(t, "pin.json"), loadCodegenFixture(t, "latest.json"))
	require.NoError(t, err)

	pin, latest := view.pin["widget"], view.latest["widget"]
	require.NotNil(t, pin)
	require.NotNil(t, latest)
	// The reference field SubnetRef is codegen's own and the primary ARN goes to
	// ackResourceMetadata, so neither is a path; `Type` keys without the `_`
	// codegen adds to Go keywords.
	assert.ElementsMatch(t, []string{"name", "type", "subnetid", "config", "config.size",
		"config.labels", "config.labels.key"}, pathKeys(pin.Spec))
	assert.ElementsMatch(t, []string{"state"}, pathKeys(pin.Status))
	assert.Equal(t, []string{"CreateWidget", "DeleteWidget", "DescribeWidget"}, latest.Ops)
	assert.Equal(t, "CreateWidget", latest.Create)

	// The ReadOne-only Region is not generated: Status comes from Create.
	assert.Equal(t, map[string]string{"priority": "Priority", "config.labels.value": "Config.Labels.Value"},
		gained(pin, latest, specSide))
	assert.Equal(t, map[string]string{"health": "Health"}, gained(pin, latest, statusSide))

	// Gizmo is ignored by resource_names; Quote has no Delete.
	assert.ElementsMatch(t, []string{"widget"}, keysOf(view.pin))
	assert.ElementsMatch(t, []string{"widget", "gadget", "quote"}, keysOf(view.latest))
	assert.True(t, view.latest["gadget"].Deletable)
	assert.False(t, view.latest["quote"].Deletable)
}

func keysOf(crds codegenCRDs) []string {
	out := make([]string, 0, len(crds))
	for k := range crds {
		out = append(out, k)
	}
	return out
}

func TestRunCodegenOracleFailures(t *testing.T) {
	pin, latest := loadCodegenFixture(t, "pin.json"), loadCodegenFixture(t, "latest.json")
	dir := t.TempDir()

	t.Run("generator.yaml is loaded strictly", func(t *testing.T) {
		path := filepath.Join(dir, "unknown-key.yaml")
		require.NoError(t, os.WriteFile(path, []byte("not_a_codegen_key: true\n"), 0o644))
		_, err := runCodegenOracle(codegenFixtureInputs(path), pin, latest)
		require.Error(t, err)
		assert.ErrorContains(t, err, "generator.yaml")
		assert.ErrorContains(t, err, "the pin model")
	})

	t.Run("a model error", func(t *testing.T) {
		path := filepath.Join(dir, "bad-from.yaml")
		require.NoError(t, os.WriteFile(path, []byte("resources:\n  Widget:\n    fields:\n      Name:\n"+
			"        from:\n          operation: DescribeWidget\n          path: Nope.Deeper\n"), 0o644))
		_, err := runCodegenOracle(codegenFixtureInputs(path), pin, latest)
		assert.ErrorContains(t, err, "Nope.Deeper")
	})

	t.Run("a panic is an error", func(t *testing.T) {
		data, err := os.ReadFile(codegenFixtures + "latest.json")
		require.NoError(t, err)
		// code-generator panics on a member whose target does not exist.
		broken, err := LoadSmithyModel([]byte(strings.Replace(string(data),
			`"target": "smithy.api#Integer"`, `"target": "com.amazonaws.widgets#Missing"`, 1)))
		require.NoError(t, err)
		_, err = runCodegenOracle(codegenFixtureInputs(codegenFixtures+"generator.yaml"), pin, broken)
		require.Error(t, err)
		assert.ErrorContains(t, err, "the latest model")
		assert.ErrorContains(t, err, "panic")
	})

	t.Run("no model document", func(t *testing.T) {
		_, err := runCodegenOracle(codegenFixtureInputs(codegenFixtures+"generator.yaml"), &SmithyModel{}, latest)
		assert.ErrorContains(t, err, "no model document")
	})
}

// widgetView is a codegen view in which Widget gains Spec `Priority` and
// `Config.Labels.Value` and Status `Health`, and Gadget is a new resource.
func widgetView() *codegenView {
	pin := &codegenCRD{
		Kind:   "Widget",
		Spec:   map[string]string{"name": "Name", "config": "Config", "config.labels": "Config.Labels"},
		Status: map[string]string{"state": "State"},
		Ops:    []string{"CreateWidget", "DeleteWidget", "DescribeWidget", "UpdateWidget"},
		Create: "CreateWidget", Deletable: true,
	}
	latest := *pin
	latest.Spec = map[string]string{"name": "Name", "config": "Config", "config.labels": "Config.Labels",
		"priority": "Priority", "config.labels.value": "Config.Labels.Value"}
	latest.Status = map[string]string{"state": "State", "health": "Health"}
	return &codegenView{
		pin: codegenCRDs{"widget": pin},
		latest: codegenCRDs{"widget": &latest, "gadget": {
			Kind: "Gadget", Ops: []string{"CreateGadget", "DeleteGadget"}, Create: "CreateGadget", Deletable: true,
		}},
	}
}

func widgetField(class FindingClass, subject, evidence string) Finding {
	return Finding{Kind: "Widget", Class: class, Subject: subject, NewSincePin: true, Evidence: evidence}
}

func findFinding(t *testing.T, findings []Finding, kind, subject string) Finding {
	t.Helper()
	for _, f := range findings {
		if f.Kind == kind && f.Subject == subject {
			return f
		}
	}
	t.Fatalf("no finding %s %s in %+v", kind, subject, findings)
	return Finding{}
}

func TestReconcileWithCodegenFields(t *testing.T) {
	in := codegenFixtureInputs("")
	gadget := Finding{Kind: "Gadget", Class: ClassNewResource, Subject: "Gadget", NewSincePin: true}
	// Each case reconciles one Widget field beside the Gadget finding, so no
	// other finding is added.
	for _, tc := range []struct {
		name   string
		f      Finding
		work   fieldWork
		detail string
	}{
		{"spec field codegen adds stays", widgetField(ClassSpecField, "Priority", "CreateWidget"), workNone, ""},
		{"nested spec field codegen adds stays",
			widgetField(ClassSpecField, "Config.Labels.Value", "CreateWidget"), workNone, ""},
		{"status field codegen adds stays", widgetField(ClassStatusField, "Health", "CreateWidget"), workNone, ""},
		{"status field may land in spec", widgetField(ClassStatusField, "Priority", "DescribeWidget"), workNone, ""},
		{"status from the resource's own read needs from:",
			widgetField(ClassStatusField, "WarmThroughput", "DescribeWidget"), workNotGenerated,
			"regeneration does not add it: codegen builds Status only from the Create response, " +
				"so it needs a field with `from: DescribeWidget`"},
		{"status from another resource's read needs a hook",
			widgetField(ClassStatusField, "ImageWatermarks", "DescribeImages"), workNotGenerated,
			"returned only by `DescribeImages`, which the generated code does not call: " +
				"needs a `from:` field or a hook"},
		{"spec only on Update needs from:",
			widgetField(ClassSpecField, "Mode", "UpdateWidget"), workNotGenerated,
			"regeneration does not add it: codegen builds Spec only from the Create request, " +
				"so it needs a field with `from: UpdateWidget`"},
		{"spec on Create that codegen still lacks",
			widgetField(ClassSpecField, "Hidden", "CreateWidget"), workNotGenerated,
			"regeneration does not add it to Spec although `CreateWidget` sends it: " +
				"check generator.yaml for an ignore or rename"},
		{"status below a spec field of the request's shape",
			widgetField(ClassStatusField, "Config.Error", "DescribeWidget"), workNotGenerated,
			"regeneration does not add it: Spec's `Config` has the Create request's shape, which lacks it, " +
				"so it needs its own Status field with `from:` or a hook"},
		{"a field that already needs work keeps its reason",
			Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Mode", NewSincePin: true,
				Evidence: "UpdateWidgetMode", Work: workDedicatedSetter, Detail: "hook"}, workDedicatedSetter, "hook"},
		{"a pre-existing field is left alone",
			Finding{Kind: "Widget", Class: ClassStatusField, Subject: "Old", Evidence: "DescribeWidget"}, workNone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := widgetView()
			// Mention the gained paths so none is added.
			mentions := []Finding{
				widgetField(ClassDroppedField, "Priority", "CreateWidget"),
				widgetField(ClassDroppedField, "Health", "CreateWidget"),
				widgetField(ClassDroppedField, "Config.Labels.Value", "CreateWidget"),
			}
			got := reconcileWithCodegen(&SmithyModel{}, append([]Finding{tc.f, gadget}, mentions...), in, view)
			require.Len(t, got, 5)
			assert.Equal(t, tc.work, got[0].Work)
			assert.Equal(t, tc.detail, got[0].Detail)
			_, needs := needsWork(got[0])
			assert.Equal(t, tc.work != workNone, needs)
		})
	}

	t.Run("a kind codegen does not generate", func(t *testing.T) {
		view := widgetView()
		delete(view.latest, "widget")
		got := reconcileWithCodegen(&SmithyModel{}, []Finding{widgetField(ClassStatusField, "Health", "CreateWidget")},
			in, view)
		assert.Equal(t, workNotGenerated, got[0].Work)
		assert.Contains(t, got[0].Detail, "code-generator generates no `Widget` CRD")
	})
}

func TestReconcileWithCodegenAddsUnreportedFields(t *testing.T) {
	in := codegenFixtureInputs("")
	gadget := Finding{Kind: "Gadget", Class: ClassNewResource, Subject: "Gadget", NewSincePin: true}

	got := reconcileWithCodegen(&SmithyModel{}, []Finding{gadget}, in, widgetView())
	var added []string
	for _, f := range got[1:] {
		assert.Equal(t, "Widget", f.Kind, "under the CRD's own kind")
		assert.Equal(t, codegenEvidence, f.Evidence)
		assert.True(t, f.NewSincePin)
		_, needs := needsWork(f)
		assert.False(t, needs, "regeneration adds it")
		added = append(added, fmt.Sprintf("%s %s", f.Class, f.Subject))
	}
	// Config.Labels.Value is its own top: Config.Labels existed at the pin.
	assert.ElementsMatch(t, []string{"spec-field Priority", "spec-field Config.Labels.Value",
		"status-field Health"}, added)

	t.Run("a child of a new path belongs to it", func(t *testing.T) {
		view := widgetView()
		view.latest["widget"].Status["health.detail"] = "Health.Detail"
		got := reconcileWithCodegen(&SmithyModel{}, []Finding{gadget}, in, view)
		assert.NotContains(t, allSubjects(got), "Health.Detail")
		assert.Contains(t, allSubjects(got), "Health")
	})

	t.Run("any mention, ancestor or child suppresses it", func(t *testing.T) {
		got := reconcileWithCodegen(&SmithyModel{}, []Finding{
			gadget,
			widgetField(ClassDroppedField, "priority", "CreateWidget"),
			widgetField(ClassLifecycleField, "Config.Labels", "UpdateWidget"),
			widgetField(ClassStatusField, "Health.Detail", "DescribeWidget"),
		}, in, widgetView())
		assert.Len(t, got, 4)
	})

	t.Run("a field the CRD already exposes", func(t *testing.T) {
		exposed := codegenFixtureInputs("")
		exposed.CRDStatusFields["Widget"]["health"] = true
		got := reconcileWithCodegen(&SmithyModel{}, []Finding{gadget}, exposed, widgetView())
		assert.NotContains(t, allSubjects(got), "Health")
	})

	t.Run("a read-back folded into Spec", func(t *testing.T) {
		// DescribeWidget returns Widget.Mode, folded into the Spec finding Mode.
		m, err := LoadSmithyModel([]byte(`{"shapes": {
			"com.amazonaws.widgets#DescribeWidget": {"type": "operation",
				"output": {"target": "com.amazonaws.widgets#DescribeWidgetResponse"}},
			"com.amazonaws.widgets#DescribeWidgetResponse": {"type": "structure",
				"members": {"Widget": {"target": "com.amazonaws.widgets#WidgetDetail"}}},
			"com.amazonaws.widgets#WidgetDetail": {"type": "structure",
				"members": {"Mode": {"target": "smithy.api#String"}}}
		}}`))
		require.NoError(t, err)
		view := widgetView()
		view.latest["widget"].Status["widget.mode"] = "Widget.Mode"
		got := reconcileWithCodegen(m, []Finding{gadget, widgetField(ClassSpecField, "Mode", "DescribeWidget,UpdateWidget")},
			in, view)
		assert.NotContains(t, allSubjects(got), "Widget.Mode")
	})
}

func allSubjects(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Subject)
	}
	return out
}

func TestReconcileWithCodegenResources(t *testing.T) {
	in := codegenFixtureInputs("")
	mentions := []Finding{
		widgetField(ClassDroppedField, "Priority", "CreateWidget"),
		widgetField(ClassDroppedField, "Health", "CreateWidget"),
		widgetField(ClassDroppedField, "Config.Labels.Value", "CreateWidget"),
	}

	t.Run("a resource codegen does not generate is dropped", func(t *testing.T) {
		gizmo := Finding{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", NewSincePin: true,
			Evidence: "CreateGizmo,DeleteGizmo"}
		got := reconcileWithCodegen(&SmithyModel{}, append([]Finding{gizmo,
			{Kind: "Gadget", Class: ClassNewResource, Subject: "Gadget", NewSincePin: true}}, mentions...), in, widgetView())
		assert.Equal(t, Finding{Class: ClassDroppedOperation, Subject: "CreateGizmo", NewSincePin: true,
			Detail: "implies `Gizmo`, but code-generator generates no CRD for it from generator.yaml"}, got[0])
		assert.Len(t, got, 5)
	})

	t.Run("an agreeing resource keeps its class; the pin decides whether it is new", func(t *testing.T) {
		view := widgetView()
		transient := Finding{Kind: "gadget", Class: ClassTransientResource, Subject: "gadget", Detail: "transient"}
		got := reconcileWithCodegen(&SmithyModel{}, append([]Finding{transient}, mentions...), in, view)
		assert.Equal(t, ClassTransientResource, got[0].Class)
		assert.True(t, got[0].NewSincePin, "not generated at the pin")
		assert.Len(t, got, 4)

		view.pin["gadget"] = view.latest["gadget"]
		got = reconcileWithCodegen(&SmithyModel{}, append([]Finding{transient}, mentions...), in, view)
		assert.False(t, got[0].NewSincePin, "generated at the pin")
	})

	t.Run("a new CRD no finding names is added", func(t *testing.T) {
		view := widgetView()
		view.latest["quote"] = &codegenCRD{Kind: "Quote", Ops: []string{"CreateQuote", "DescribeQuotes"},
			Create: "CreateQuote"}
		got := reconcileWithCodegen(&SmithyModel{}, mentions, in, view)
		require.Len(t, got, 5)
		assert.Equal(t, Finding{Kind: "Gadget", Class: ClassNewResource, Subject: "Gadget", NewSincePin: true,
			Detail: "code-generator generates a CRD for it", Evidence: "CreateGadget,DeleteGadget"}, got[3])
		assert.Equal(t, ClassTransientResource, got[4].Class, "nothing deletes it")
		assert.Equal(t, "Quote", got[4].Subject)
	})

	t.Run("a possible resource stays and is not added twice", func(t *testing.T) {
		possible := Finding{Kind: "Gadget", Class: ClassPossibleResource, Subject: "Gadget", NewSincePin: true}
		got := reconcileWithCodegen(&SmithyModel{}, append([]Finding{possible}, mentions...), in, widgetView())
		assert.Equal(t, possible, got[0])
		assert.Len(t, got, 4)
	})
}

func TestNotGeneratedFieldRendersUnderNeedsWork(t *testing.T) {
	f := widgetField(ClassStatusField, "WarmThroughput", "DescribeWidget")
	got := reconcileWithCodegen(&SmithyModel{}, []Finding{f}, codegenFixtureInputs(""), &codegenView{
		pin: codegenCRDs{}, latest: codegenCRDs{"widget": widgetView().latest["widget"]},
	})
	body, _ := renderIssueBody("widgets", "v1.0.0", "v1.1.0", got[:1])
	needs := strings.Index(body, "### "+needsWorkHeader)
	require.GreaterOrEqual(t, needs, 0)
	assert.Contains(t, body[needs:], "`WarmThroughput` (Status) — regeneration does not add it")
	assert.NotContains(t, body, regenerateHeader+"\n")
}

func TestParseComparedVersionsAcceptsBothFooters(t *testing.T) {
	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Contains(t, body, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0 with code-generator "+codeGeneratorVersion+"\n")
	for name, b := range map[string]string{
		"current": body,
		// Filed before the footer named code-generator.
		"earlier": strings.Replace(body, " with code-generator "+codeGeneratorVersion, "", 1),
	} {
		baseline, latest, ok := parseComparedVersions(b)
		require.True(t, ok, name)
		assert.Equal(t, "v1.41.5", baseline, name)
		assert.Equal(t, "v1.44.0", latest, name)
	}
}

func TestCodeGeneratorReleaseWarning(t *testing.T) {
	serve := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/repos/aws-controllers-k8s/code-generator/releases/latest", r.URL.Path)
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}
	}
	ctx := context.Background()

	client := newRawTestGitHubClient(t, serve(http.StatusOK, `{"tag_name": "v0.65.0"}`))
	assert.Contains(t, codeGeneratorReleaseWarning(ctx, client, "v0.64.0"),
		"code-generator v0.65.0 is released but this binary runs v0.64.0")

	client = newRawTestGitHubClient(t, serve(http.StatusOK, `{"tag_name": "v0.64.0"}`))
	assert.Empty(t, codeGeneratorReleaseWarning(ctx, client, "v0.64.0"))

	client = newRawTestGitHubClient(t, serve(http.StatusInternalServerError, `{}`))
	assert.Contains(t, codeGeneratorReleaseWarning(ctx, client, "v0.64.0"), "unable to check")
}
