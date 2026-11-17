package prowlaunch

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// ModeBump and ModeRefresh are sdk-bump's modes (Plan 6 sdkbump.ModeBump,
// sdkbump.ModeRefresh; a test keeps them equal).
const (
	ModeBump    = "bump"
	ModeRefresh = "refresh"
)

// validators lists every arg the Dispatcher may pass and what its value must
// look like. Args reach the pod as container args or environment variables,
// never through a shell, and are still kept to these shapes. An arg a
// workflow accepts but that has no validator here (add-field's model,
// aws-sdk-version) can never be passed by the Dispatcher.
var validators = map[string]*regexp.Regexp{
	"service":       regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`),
	"resource":      regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,127}$`),
	"field":         regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z][A-Za-z0-9]*)*$`),
	"work-item":     regexp.MustCompile(`^sha256:[0-9a-f]{16}$`),
	"change-class":  regexp.MustCompile(`^(SDK-bump|config-only)$`),
	"sdk-to":        regexp.MustCompile(`^service/[a-z0-9]{1,63}/v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`),
	"detector-work": regexp.MustCompile(`^(create-only|not-generated|server-default)$`),
	"mode":          regexp.MustCompile(`^(` + ModeBump + `|` + ModeRefresh + `)$`),
}

// maxArgLen bounds every arg value.
const maxArgLen = 256

// classOf is the only change class each workflow is started for.
var classOf = map[string]string{"add-field": "config-only", "sdk-bump": "SDK-bump"}

// checkArgs checks args against the workflow's required_args and
// optional_args (as agent-plugin's ValidateRequiredArgs and ValidateArgs do
// for /agent) and against validators. workItem is the key hash the launch id
// names; the work-item arg must equal it.
func checkArgs(name string, w Workflow, args map[string]string, workItem string) error {
	for _, a := range w.RequiredArgs {
		if _, ok := args[a]; !ok {
			return fmt.Errorf("missing required arg %q", a)
		}
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(w.RequiredArgs, k) && !slices.Contains(w.OptionalArgs, k) {
			return fmt.Errorf("arg %q is not in the workflow's required_args or optional_args", k)
		}
		re, ok := validators[k]
		if !ok {
			return fmt.Errorf("arg %q is not one the Dispatcher may pass", k)
		}
		if len(args[k]) > maxArgLen || !re.MatchString(args[k]) {
			return fmt.Errorf("arg %q has a value of the wrong shape (len=%d)", k, len(args[k]))
		}
	}
	if args["work-item"] != workItem {
		return fmt.Errorf("arg work-item must be the launch id's key hash %s", workItem)
	}
	if args["change-class"] != classOf[name] {
		return fmt.Errorf("arg change-class must be %q for %s", classOf[name], name)
	}
	return nil
}
