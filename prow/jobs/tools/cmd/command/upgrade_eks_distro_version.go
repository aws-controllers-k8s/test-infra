package command

import (
	"fmt"
	"log"
	"strings"

	"github.com/spf13/cobra"
)

var (
	OptEksDistroEcrRepository string
)

var upgradeEksDistroCMD = &cobra.Command{
	Use:   "upgrade-eks-distro-version",
	Short: "upgrade-eks-distro-version - queries ecr for latest eks-distro version and patches prow images if there's an update",
	RunE:  runUpgradeEksDistro,
}

func init() {
	upgradeEksDistroCMD.PersistentFlags().StringVar(
		&OptEksDistroEcrRepository, "eks-distro-ecr-repository", "v2/eks-distro-build-tooling/eks-distro-minimal-base-nonroot", "ecr gallery repository for eks-distro",
	)
	upgradeEksDistroCMD.PersistentFlags().StringVar(
		&OptBuildConfigPath, "build-config-path", "./build_config.yaml", "path to build_config.yaml, where all the build versions are stored",
	)
	rootCmd.AddCommand(upgradeEksDistroCMD)
}

// runUpgradeEksDistro command queries ECR for the latest eks-distro version
// if the one in build_config.yaml is outdated we use the latest version,
// and patch all prow images in images_config.yaml
func runUpgradeEksDistro(cmd *cobra.Command, args []string) error {

	log.SetPrefix("upgrade-eks-distro-version: ")

	ecrEksDistroVersion, err := listRepositoryTags(OptEksDistroEcrRepository)
	if err != nil {
		return fmt.Errorf("unable to get eks-distro versions from %s: %v", OptEksDistroEcrRepository, err)
	}
	log.Printf("Successfully listed eks-distro versions from %s", OptEksDistroEcrRepository)

	highestEcrEksDistroVersion, err := findHighestEcrEksDistroVersion(ecrEksDistroVersion)
	if err != nil {
		return err
	}
	log.Printf("Highest EKS Distro version: %s\n", highestEcrEksDistroVersion)

	buildConfigData, err := readBuildConfigFile(OptBuildConfigPath)
	if err != nil {
		return err
	}
	log.Printf("Build Config EKS Distro version: %s\n", buildConfigData.EksDistroVersion)

	needUpgrade := eksDistroVersionIsGreaterThan(highestEcrEksDistroVersion, buildConfigData.EksDistroVersion)
	if !needUpgrade {
		log.Println("eks-distro version is up-to-date")
		return nil
	}

	log.Printf("Updating eks-distro version to %s\n", highestEcrEksDistroVersion)
	olderVersion := buildConfigData.EksDistroVersion
	buildConfigData.EksDistroVersion = highestEcrEksDistroVersion
	if err = patchBuildVersionFile(buildConfigData, OptBuildConfigPath); err != nil {
		return err
	}
	log.Printf("Successfully updated eks-distro version in build_config")

	commitBranch := fmt.Sprintf(updateEksDistroPRCommitBranch, highestEcrEksDistroVersion)
	prSubject := fmt.Sprintf(updateEksDistroPRSubject, highestEcrEksDistroVersion)
	prDescription := fmt.Sprintf(updateEksDistroPRDescription, olderVersion, highestEcrEksDistroVersion)

	log.Println("Commiting changes and creating PR")
	err = commitAndSendPR(OptSourceOwner, OptSourceRepo, commitBranch, updateEksDistroSourceFiles, baseBranch, prSubject, prDescription)
	if err != nil && !strings.Contains(err.Error(), "pull request already exists") {
		return err
	}
	return nil
}
