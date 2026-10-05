// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package generator

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// Structure for prow job versions stored in
// images_config.yaml
type ImageContext struct {
	ImageRepo string            `yaml:"image_repo"`
	Images    map[string]string `yaml:"images"`
}

// Structure for prow job configurations stored in
// jobs.yaml
type JobsConfig struct {
	PeriodicsEnabled              bool     `yaml:"periodics_enabled"`
	AWSServices                   []string `yaml:"aws_services"`
	CarmTestServices              []string `yaml:"carm_test_services"`
	IRSTestServices               []string `yaml:"irs_test_services"`
	SoakTestOnReleaseServiceNames []string `yaml:"soak_test_on_release_service_names"`
	CodegenPresubmitServices      []string `yaml:"code_gen_presubmit_services"`
	RuntimePresubmitServices      []string `yaml:"runtime_presubmit_services"`
	ACKTestPresubmitServices      []string `yaml:"acktest_presubmit_services"`
	APINotificationServices       []string `yaml:"api_notification_services"`
	APINotificationMaxOpenIssues  int      `yaml:"api_notification_max_open_issues"`
	// PresubmitCluster routes pre-submit jobs to a named Prow build cluster
	// (the kubeconfig context name, e.g. "build"). Empty => jobs omit the
	// `cluster:` field and run on the implicit in-cluster "default" cluster.
	PresubmitCluster string `yaml:"presubmit_cluster"`
}

func loadImages(imageConfigPath string) (*ImageContext, error) {

	fileData, err := os.ReadFile(imageConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %s: %v", imageConfigPath, err)
	}

	var imageContext *ImageContext
	if err = yaml.Unmarshal(fileData, &imageContext); err != nil {
		return nil, fmt.Errorf("unable to unmarshall imageConfig: %v", err)
	}
	return imageContext, nil
}

func loadConfig(configPath string) (*JobsConfig, error) {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %s: %v", configPath, err)
	}

	var config *JobsConfig
	if err = yaml.Unmarshal(fileData, &config); err != nil {
		return nil, fmt.Errorf("unable to unmarshall imageConfig: %v", err)
	}
	if err := validateJobsConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}

// validateJobsConfig rejects configurations that would generate broken jobs.
//
// TODO: CodegenPresubmitServices, RuntimePresubmitServices and
// ACKTestPresubmitServices carry the identical implicit invariant and are
// unguarded: each is ranged over to emit `extra_refs` pointing at a
// `{{ $service }}-controller` repo, so a typo in any of them generates a job
// targeting a repo that does not exist. Extend this hook to cover them rather
// than adding a parallel validator.
func validateJobsConfig(config *JobsConfig) error {
	// loadConfig declares `var config *JobsConfig` and yaml.Unmarshal leaves it
	// nil for a document that parses to nothing — an empty file, a comment-only
	// file, or an explicit `null`. This is now the first dereference, so say so
	// here rather than panicking further down.
	if config == nil {
		return fmt.Errorf("jobs_config.yaml parsed to nothing; it is empty or contains only comments")
	}

	if len(config.APINotificationServices) == 0 {
		return nil
	}

	if err := ValidateAPINotificationServices(config.APINotificationServices, config.AWSServices); err != nil {
		return err
	}

	// Zero is not treated as "unlimited": defaulting to no protection would
	// mean anyone adding services without considering the cap silently loses
	// it, which defeats the point of having one.
	if config.APINotificationMaxOpenIssues <= 0 {
		return fmt.Errorf(
			"api_notification_max_open_issues must be greater than zero when " +
				"api_notification_services is non-empty",
		)
	}

	// A cap above the notified-list length is a typo, not a policy: the run files at
	// most one issue per notified service, so the issues *this run would file* can
	// never reach it. It is reachable only by duplicate or stale issues, which is not
	// a limit anyone sets deliberately — and the cap is the only thing bounding how
	// much a bad first run writes to a public repo. Someone who genuinely wants no cap
	// should say so by shortening api_notification_services, not by picking a big
	// number.
	//
	// Strictly greater, not >=, for that same reason read the other way: openCount
	// counts every open issue this detector owns repo-wide — duplicates for one
	// service, and stale ones for services since removed from the list — so a cap
	// equal to the list length is genuinely reachable and does bind.
	if config.APINotificationMaxOpenIssues > len(config.APINotificationServices) {
		return fmt.Errorf(
			"api_notification_max_open_issues is %d, which cannot bind: only %d service(s) "+
				"are listed in api_notification_services, so the run can never file that many issues",
			config.APINotificationMaxOpenIssues, len(config.APINotificationServices))
	}
	return nil
}

// ValidateAPINotificationServices checks the service list the API change detector will
// act on. Exported because generation-time validation does not guard the path that
// matters: the running job reads jobs_config.yaml itself, through extra_refs or the
// jobs-config ConfigMap, so `make prow-gen` is not on the path at all — and
// createGithubIssueWithClient's post-create check verifies only the ownership label,
// never `service/<svc>`. Unvalidated, a service outside aws_services files a real issue
// in a public repo carrying a label that exists in no config file.
//
// Deliberately narrower than validateJobsConfig: it omits the cap, because the cap is
// already enforced at the point of use by reconcileIssue and because the running job
// takes its cap from a flag rather than from this file. Validating a field the command
// does not read would fail runs over a discrepancy that cannot affect them.
func ValidateAPINotificationServices(services, awsServices []string) error {
	// The issues this job files are labelled service/<svc>, and those labels are
	// only generated for entries in aws_services. A service outside that list also
	// has no controller pinned by this repo whose aws-sdk-go-v2 version and generated
	// code the job could diff. The message deliberately does not claim the controller
	// does not exist — the ACK org has more controllers than this list has entries —
	// only that this repo has none to diff. Either way the fix is to onboard the
	// service here, not to hand-create the label.
	//
	// This one check also rejects empty entries, case mismatches ("S3") and untrimmed
	// whitespace (" s3 ") for free, since none of those appear in aws_services; kept
	// that way deliberately rather than adding separate checks for each.
	for _, service := range services {
		if !contains(awsServices, service) {
			return fmt.Errorf(
				"api_notification_services lists %q, which is not in aws_services, so the job "+
					"has no pinned controller to diff and no service/%s label to apply; "+
					"onboard it in aws_services first",
				service, service,
			)
		}
	}

	seen := make(map[string]bool, len(services))
	for _, service := range services {
		if seen[service] {
			return fmt.Errorf(
				"api_notification_services lists %q more than once; the run resolves each "+
					"service's existing issue once, so a repeat would file a second issue for it",
				service)
		}
		seen[service] = true
	}
	return nil
}

func contains(arr []string, s string) bool {
	return slices.Contains(arr, s)
}

func loadTemplates(prowJobType, templateDir string, imageContext *ImageContext, config *JobsConfig) (string, error) {

	templateFiles, err := os.ReadDir(templateDir)
	if err != nil {
		return "", fmt.Errorf("unable to read directory %s: %v", templateDir, err)
	}

	var content strings.Builder

	content.WriteString(fmt.Sprintf("%s:\n", prowJobType))
	data := map[string]interface{}{
		"Config":       config,
		"ImageContext": imageContext,
	}

	for _, file := range templateFiles {
		// templateContent, err := template.ParseFS(filename, imageContext.Images[0])
		fileData, _ := os.ReadFile(fmt.Sprintf("%s/%s", templateDir, file.Name()))
		tmpl, err := template.New(file.Name()).Funcs(template.FuncMap{"contains": contains}).Parse(string(fileData))

		if err != nil {
			panic(err)
		}
		err = tmpl.Execute(&content, data)
		if err != nil {
			panic(err)
		}
		content.WriteString("\n")
	}
	return content.String(), err
}

func generateJobs(templatePath, outputPath string, imageContext *ImageContext, config *JobsConfig) error {
	periodicJobsPath := fmt.Sprintf("%s/periodics", templatePath)
	postsubmitJobsPath := fmt.Sprintf("%s/postsubmits", templatePath)
	presubmitJobsPath := fmt.Sprintf("%s/presubmits", templatePath)

	var prowjobsContent strings.Builder
	prowjobsContent.WriteString("# Autogenerated. Do NOT update Manually.\n")
	prowjobsContent.WriteString(fmt.Sprintf("# Last generated on %v\n", time.Now().Format(time.DateTime)))

	var periodicsContent string
	var err error
	if config.PeriodicsEnabled {
		periodicsContent, err = loadTemplates("periodics", periodicJobsPath, imageContext, config)
		if err != nil {
			return err
		}
	} else {
		periodicsContent = "periodics: []\n"
	}
	postSubmitContent, err := loadTemplates("postsubmits", postsubmitJobsPath, imageContext, config)
	if err != nil {
		return err
	}
	presubmitContent, err := loadTemplates("presubmits", presubmitJobsPath, imageContext, config)
	if err != nil {
		return err
	}

	prowjobsContent.WriteString(fmt.Sprintf("%s%s%s", periodicsContent, postSubmitContent, presubmitContent))

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	_, err = file.WriteString(prowjobsContent.String())
	return err
}

func generateLabelSyncConfig(templatePath, outputPath string, config *JobsConfig) error {

	var configContent strings.Builder
	configContent.WriteString("# Autogenerated. Do NOT update Manually\n")
	configContent.WriteString(fmt.Sprintf("# Last generated on %v.\n#\n", time.Now().Format(time.DateTime)))

	tmpl, err := template.ParseFiles(fmt.Sprintf("%s/config/label_sync.tpl", templatePath))
	if err != nil {
		return err
	}
	err = tmpl.Execute(&configContent, config)
	if err != nil {
		return err
	}
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}

	_, err = file.WriteString(configContent.String())
	return err
}

func addAutoGenHeader(content *strings.Builder) {
	content.WriteString("# Autogenerated. Do NOT update Manually\n")
	fmt.Fprintf(content, "# Last generated on %v.\n#\n", time.Now().Format(time.DateTime))
}

// Generate will generate labels or jobs, depending on variable "what" is.
// what: can be either labels or jobs.
func Generate(what, jobsConfigPath, imagesConfigPath, templatePath, outputPath string) error {
	config, err := loadConfig(jobsConfigPath)
	if err != nil {
		return err
	}

	imageContext, err := loadImages(imagesConfigPath)
	if err != nil {
		return err
	}

	switch what {
	case "jobs":
		err = generateJobs(templatePath, outputPath, imageContext, config)
	case "labels":
		err = generateLabelSyncConfig(templatePath, outputPath, config)
	}
	return err
}

// GenerateManifest processes a single template file with image context and
// writes the result to outputPath. Used for standalone manifests like
// job-config-job.yaml that aren't part of the prow jobs/presubmits/postsubmits
// structure but still need image tag substitution.
func GenerateManifest(imagesConfigPath, templatePath, outputPath string) error {
	imageContext, err := loadImages(imagesConfigPath)
	if err != nil {
		return err
	}

	fileData, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("unable to read template %s: %v", templatePath, err)
	}

	tmpl, err := template.New("manifest").Funcs(template.FuncMap{"contains": contains}).Parse(string(fileData))
	if err != nil {
		return fmt.Errorf("unable to parse template %s: %v", templatePath, err)
	}

	data := map[string]interface{}{
		"ImageContext": imageContext,
	}

	var content strings.Builder
	addAutoGenHeader(&content)

	if err := tmpl.Execute(&content, data); err != nil {
		return fmt.Errorf("unable to execute template %s: %v", templatePath, err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(content.String())
	return err
}

func GenerateAgentWorkflows(imagesConfigPath, templatePath, outputPath string) error {
	imageContext, err := loadImages(imagesConfigPath)
	if err != nil {
		return err
	}

	data := map[string]interface{}{
		"ImageContext": imageContext,
	}

	templateFiles, err := os.ReadDir(templatePath)
	if err != nil {
		return fmt.Errorf("unable to read directory %s: %v", templatePath, err)
	}

	var content strings.Builder
	addAutoGenHeader(&content)
	content.WriteString("workflows:\n")

	for _, file := range templateFiles {
		// Only process Go template files (.tpl). The templates directory also
		// holds Helm chart templates (e.g. agent-workflow-config.yaml) that use
		// Helm/Sprig functions like "required" and must not be parsed here.
		if !strings.HasSuffix(file.Name(), ".tpl") {
			continue
		}

		fileData, _ := os.ReadFile(fmt.Sprintf("%s/%s", templatePath, file.Name()))
		tmpl, err := template.New(file.Name()).Funcs(template.FuncMap{"contains": contains}).Parse(string(fileData))

		if err != nil {
			panic(err)
		}
		err = tmpl.Execute(&content, data)
		if err != nil {
			panic(err)
		}
		content.WriteString("\n")
	}

	file, err := os.Create(outputPath)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return err
	}

	_, err = file.WriteString(content.String())
	return err
}

func GeneratePlugins(
	imagesConfigPath,
	templatePath,
	outputPath string,
) ([]string, error) {
	imageContext, err := loadImages(imagesConfigPath)
	if err != nil {
		return nil, err
	}

	data := map[string]interface{}{
		"ImageContext": imageContext,
	}

	generatedFiles := make([]string, 0)
	for pluginName := range imageContext.Images {

		templateDir := fmt.Sprintf("%s/%s", templatePath, pluginName)
		outputDir := fmt.Sprintf("%s/%s", outputPath, pluginName)

		templateFiles, err := os.ReadDir(templateDir)
		if err != nil {
			return nil, fmt.Errorf("unable to read directory %s: %v", templateDir, err)
		}
		for _, file := range templateFiles {
			var content strings.Builder
			addAutoGenHeader(&content)

			fileData, _ := os.ReadFile(fmt.Sprintf("%s/%s", templateDir, file.Name()))
			tmpl, err := template.New(file.Name()).Funcs(template.FuncMap{"contains": contains}).Parse(string(fileData))

			if err != nil {
				panic(err)
			}
			err = tmpl.Execute(&content, data)
			if err != nil {
				panic(err)
			}

			outputFileName := strings.TrimSuffix(file.Name(), ".tpl") + ".yaml"
			templateOutputPath := fmt.Sprintf("%s/%s", outputDir, outputFileName)
			file, err := os.Create(templateOutputPath)
			if file != nil {
				defer file.Close()
			}
			if err != nil {
				return nil, err
			}

			_, err = file.WriteString(content.String())
			if err != nil {
				return nil, err
			}

			generatedFiles = append(generatedFiles, templateOutputPath)
		}
	}

	return generatedFiles, nil
}
