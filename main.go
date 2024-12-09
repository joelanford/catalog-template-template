package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"github.com/blang/semver/v4"
	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/operator-framework/operator-registry/alpha/declcfg"
	"github.com/operator-framework/operator-registry/alpha/property"
	"github.com/spf13/cobra"
	"io"
	"k8s.io/apimachinery/pkg/util/sets"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"slices"
	"strconv"
	"strings"
	"text/template"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		slog.Error("could not execute root command", "error", err.Error())
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var registryNamespaceFlag string
	cmd := &cobra.Command{
		Use:  "catalog-template-template <packageDir>",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			packageDir := args[0]

			registryNamespace := os.Getenv("CTT_REGISTRY_NAMESPACE")
			if cmd.Flag("registry-namespace").Changed {
				registryNamespace = registryNamespaceFlag
			}
			if registryNamespace == "" {
				slog.Error("registry namespace must be set with --registry-namespace flag or CTT_REGISTRY_NAMESPACE environment variable")
				os.Exit(1)
			}

			if err := run(packageDir, registryNamespace); err != nil {
				slog.Error(err.Error())
				os.Exit(1)
			}
		},
	}
	cmd.Flags().StringVar(&registryNamespaceFlag, "registry-namespace", "", "The registry namespace (e.g. quay.io/operatorhubio)")
	return cmd
}

func run(packageDir, registryNamespace string) error {
	packageDirs, err := os.ReadDir(packageDir)
	if err != nil {
		return fmt.Errorf("could not read package directory %s: %v", packageDir, err)
	}

	var bundles []Bundle
	for _, dirEntry := range packageDirs {
		if !dirEntry.IsDir() || dirEntry.Name() == "catalog-templates" {
			continue
		}

		bundleDir := filepath.Join(packageDir, dirEntry.Name())
		b, err := buildBundle(bundleDir, registryNamespace)
		if err != nil {
			return fmt.Errorf("could not build bundle %s: %v", bundleDir, err)
		}

		bundles = append(bundles, *b)
	}

	allCatalogVersions := sets.New[CatalogVersion]()
	for _, b := range bundles {
		allCatalogVersions = allCatalogVersions.Union(b.catalogVersions)
	}

	bundlesForCatalog := make(map[CatalogVersion][]Bundle, len(allCatalogVersions))
	for _, b := range bundles {
		for cv := range b.catalogVersions {
			bundlesForCatalog[cv] = append(bundlesForCatalog[cv], b)
		}
	}

	templateFilePath := filepath.Join(packageDir, "fbc-template.yaml.tmpl")
	templateFileContents, err := os.ReadFile(templateFilePath)
	if err != nil {
		return fmt.Errorf("could not read template file %s: %v", templateFilePath, err)
	}
	tpl, err := template.New("template").Funcs(sprig.HermeticTxtFuncMap()).Parse(string(templateFileContents))
	if err != nil {
		return fmt.Errorf("could not parse template from %s: %v", templateFilePath, err)
	}

	var templateValues map[string]interface{}
	templateValuesFilePath := filepath.Join(packageDir, "fbc-template.values.yaml")
	templateValuesContents, err := os.ReadFile(templateValuesFilePath)
	if err != nil {
		return fmt.Errorf("could not read template values from %s: %v", templateValuesFilePath, err)
	}
	if err := yaml.Unmarshal(templateValuesContents, &templateValues); err != nil {
		return fmt.Errorf("could not parse template values from %s: %v", templateValuesFilePath, err)
	}

	sortedVersions := allCatalogVersions.UnsortedList()
	slices.SortFunc(sortedVersions, func(a, b CatalogVersion) int {
		if cmpMajor := cmp.Compare(a.Major, b.Major); cmpMajor != 0 {
			return cmpMajor
		}
		return cmp.Compare(a.Minor, b.Minor)
	})

	for _, cv := range sortedVersions {
		catalogBundles := bundlesForCatalog[cv]
		slices.SortFunc(catalogBundles, func(a, b Bundle) int {
			return a.Version.Compare(b.Version)
		})
		td := TemplateData{
			CatalogVersion: cv,
			Bundles:        catalogBundles,
			Values:         templateValues,
		}

		outFilePath := filepath.Join(packageDir, "catalog-templates", fmt.Sprintf("v%s.yaml", cv.String))
		if err := os.MkdirAll(filepath.Dir(outFilePath), 0755); err != nil {
			return fmt.Errorf("could not create output directory %s: %v", filepath.Dir(outFilePath), err)
		}
		outFile, err := os.Create(outFilePath)
		if err != nil {
			return fmt.Errorf("could not create output file %s: %v", outFilePath, err)
		}
		defer outFile.Close()

		if err := tpl.Execute(outFile, td); err != nil {
			return fmt.Errorf("could not execute template: %v", err)
		}
		fmt.Println("Wrote template:", outFilePath)
	}
	return nil
}

type TemplateData struct {
	CatalogVersion CatalogVersion
	Bundles        []Bundle
	Values         map[string]interface{}
}

type CatalogVersion struct {
	String string
	Major  int
	Minor  int
}

type Bundle struct {
	Name    string
	Version semver.Version
	Image   string

	catalogVersions sets.Set[CatalogVersion]
}

type BundleReleaseConfig struct {
	CatalogVersions []string `yaml:"catalogVersions"`
}

func buildBundle(bundleDir, registryNamespace string) (*Bundle, error) {
	absBundleDir, err := filepath.Abs(bundleDir)
	if err != nil {
		return nil, err
	}

	tmpSpecFile, err := os.CreateTemp("", fmt.Sprintf("build-bundle-%s-*", filepath.Base(bundleDir)))
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpSpecFile.Name())

	specFileString := fmt.Sprintf(`apiVersion: specs.kpm.io/v1
kind: Bundle
bundleRoot: %s
registryNamespace: %s
`, absBundleDir, registryNamespace)
	if _, err := tmpSpecFile.WriteString(specFileString); err != nil {
		return nil, err
	}
	if err := tmpSpecFile.Close(); err != nil {
		return nil, err
	}

	buildOut, err := exec.Command("kpm", "build", "bundle", tmpSpecFile.Name()).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("exec: kpm build bundle: %v", err)
	}
	var kpmFileName string
	for _, word := range strings.Split(string(buildOut), " ") {
		if strings.HasSuffix(word, "bundle.kpm") {
			kpmFileName = word
			break
		}
	}
	if kpmFileName == "" {
		return nil, fmt.Errorf("could not find kpm bundle in build output for bundle %q", bundleDir)
	}
	defer os.Remove(kpmFileName)

	var buf bytes.Buffer
	renderCmd := exec.Command("kpm", "render", kpmFileName)
	renderCmd.Stderr = io.Discard
	renderCmd.Stdout = &buf
	if err := renderCmd.Run(); err != nil {
		return nil, fmt.Errorf("exec: kpm render: %v", err)
	}
	renderOut := buf.Bytes()

	var b declcfg.Bundle
	if err := json.Unmarshal(renderOut, &b); err != nil {
		return nil, err
	}

	name := b.Name
	image := b.Image

	var version string
	for _, p := range b.Properties {
		if p.Type != "olm.package" {
			continue
		}
		var pkg property.Package
		if err := json.Unmarshal(p.Value, &pkg); err != nil {
			return nil, err
		}
		version = pkg.Version
		break
	}

	releaseConfigData, err := os.ReadFile(filepath.Join(bundleDir, "release-config.yaml"))
	if err != nil {
		return nil, err
	}
	var rc BundleReleaseConfig
	if err := yaml.Unmarshal(releaseConfigData, &rc); err != nil {
		return nil, err
	}

	catalogVersions := sets.New[CatalogVersion]()
	for _, cv := range rc.CatalogVersions {
		splits := strings.Split(cv, ".")
		if len(splits) != 2 {
			return nil, fmt.Errorf("invalid catalog version %q, expected '<major>.<minor>'", cv)
		}
		for _, s := range splits {
			if len(s) > 1 && strings.HasPrefix(s, "0") {
				return nil, fmt.Errorf("invalid catalog version %q, leading zeroes in version numbers are not permitted", cv)
			}
		}
		major, err := strconv.Atoi(splits[0])
		if err != nil {
			return nil, fmt.Errorf("invalid catalog version major version %q, expected integer", splits[0])
		}
		if major < 0 {
			return nil, fmt.Errorf("invalid catalog version major version %q, cannot be negative", splits[1])
		}
		minor, err := strconv.Atoi(splits[1])
		if err != nil {
			return nil, fmt.Errorf("invalid catalog version minor version %q, expected integer", splits[1])
		}
		if minor < 0 {
			return nil, fmt.Errorf("invalid catalog version minor version %q, cannot be negative", splits[1])
		}
		catalogVersions.Insert(CatalogVersion{
			String: cv,
			Major:  major,
			Minor:  minor,
		})
	}

	semverVersion, err := semver.Parse(version)
	if err != nil {
		return nil, err
	}

	return &Bundle{
		Name:    name,
		Image:   image,
		Version: semverVersion,

		catalogVersions: catalogVersions,
	}, nil
}
