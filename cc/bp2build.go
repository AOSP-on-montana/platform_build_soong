// Copyright 2021 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cc

import (
	"fmt"
	"path/filepath"
	"strings"

	"android/soong/android"
	"android/soong/bazel"
	"android/soong/cc/config"

	"github.com/google/blueprint"

	"github.com/google/blueprint/proptools"
)

const (
	cSrcPartition     = "c"
	asSrcPartition    = "as"
	asmSrcPartition   = "asm"
	lSrcPartition     = "l"
	llSrcPartition    = "ll"
	cppSrcPartition   = "cpp"
	protoSrcPartition = "proto"
	aidlSrcPartition  = "aidl"
)

// staticOrSharedAttributes are the Bazel-ified versions of StaticOrSharedProperties --
// properties which apply to either the shared or static version of a cc_library module.
type staticOrSharedAttributes struct {
	Srcs      bazel.LabelListAttribute
	Srcs_c    bazel.LabelListAttribute
	Srcs_as   bazel.LabelListAttribute
	Srcs_aidl bazel.LabelListAttribute
	Hdrs      bazel.LabelListAttribute
	Copts     bazel.StringListAttribute

	Deps                              bazel.LabelListAttribute
	Implementation_deps               bazel.LabelListAttribute
	Dynamic_deps                      bazel.LabelListAttribute
	Implementation_dynamic_deps       bazel.LabelListAttribute
	Whole_archive_deps                bazel.LabelListAttribute
	Implementation_whole_archive_deps bazel.LabelListAttribute
	Runtime_deps                      bazel.LabelListAttribute

	System_dynamic_deps bazel.LabelListAttribute

	Enabled bazel.BoolAttribute

	Native_coverage bazel.BoolAttribute

	sdkAttributes
}

// groupSrcsByExtension partitions `srcs` into groups based on file extension.
func groupSrcsByExtension(ctx android.BazelConversionPathContext, srcs bazel.LabelListAttribute) bazel.PartitionToLabelListAttribute {
	// Convert filegroup dependencies into extension-specific filegroups filtered in the filegroup.bzl
	// macro.
	addSuffixForFilegroup := func(suffix string) bazel.LabelMapper {
		return func(otherModuleCtx bazel.OtherModuleContext, label bazel.Label) (string, bool) {

			m, exists := otherModuleCtx.ModuleFromName(label.OriginalModuleName)
			labelStr := label.Label
			if !exists || !android.IsFilegroup(otherModuleCtx, m) {
				return labelStr, false
			}
			// If the filegroup is already converted to aidl_library, skip creating
			// _c_srcs, _as_srcs, _cpp_srcs filegroups
			fg, _ := m.(android.Bp2buildAidlLibrary)
			if fg.ShouldConvertToAidlLibrary(ctx) {
				return labelStr, false
			}
			return labelStr + suffix, true
		}
	}

	// TODO(b/190006308): Handle language detection of sources in a Bazel rule.
	labels := bazel.LabelPartitions{
		protoSrcPartition: android.ProtoSrcLabelPartition,
		cSrcPartition:     bazel.LabelPartition{Extensions: []string{".c"}, LabelMapper: addSuffixForFilegroup("_c_srcs")},
		asSrcPartition:    bazel.LabelPartition{Extensions: []string{".s", ".S"}, LabelMapper: addSuffixForFilegroup("_as_srcs")},
		asmSrcPartition:   bazel.LabelPartition{Extensions: []string{".asm"}},
		aidlSrcPartition:  android.AidlSrcLabelPartition,
		// TODO(http://b/231968910): If there is ever a filegroup target that
		// 		contains .l or .ll files we will need to find a way to add a
		// 		LabelMapper for these that identifies these filegroups and
		//		converts them appropriately
		lSrcPartition:  bazel.LabelPartition{Extensions: []string{".l"}},
		llSrcPartition: bazel.LabelPartition{Extensions: []string{".ll"}},
		// C++ is the "catch-all" group, and comprises generated sources because we don't
		// know the language of these sources until the genrule is executed.
		cppSrcPartition: bazel.LabelPartition{Extensions: []string{".cpp", ".cc", ".cxx", ".mm"}, LabelMapper: addSuffixForFilegroup("_cpp_srcs"), Keep_remainder: true},
	}

	return bazel.PartitionLabelListAttribute(ctx, &srcs, labels)
}

// bp2BuildParseLibProps returns the attributes for a variant of a cc_library.
func bp2BuildParseLibProps(ctx android.BazelConversionPathContext, module *Module, isStatic bool) staticOrSharedAttributes {
	lib, ok := module.compiler.(*libraryDecorator)
	if !ok {
		return staticOrSharedAttributes{}
	}
	return bp2buildParseStaticOrSharedProps(ctx, module, lib, isStatic)
}

// bp2buildParseSharedProps returns the attributes for the shared variant of a cc_library.
func bp2BuildParseSharedProps(ctx android.BazelConversionPathContext, module *Module) staticOrSharedAttributes {
	return bp2BuildParseLibProps(ctx, module, false)
}

// bp2buildParseStaticProps returns the attributes for the static variant of a cc_library.
func bp2BuildParseStaticProps(ctx android.BazelConversionPathContext, module *Module) staticOrSharedAttributes {
	return bp2BuildParseLibProps(ctx, module, true)
}

type depsPartition struct {
	export         bazel.LabelList
	implementation bazel.LabelList
}

type bazelLabelForDepsFn func(android.BazelConversionPathContext, []string) bazel.LabelList

func maybePartitionExportedAndImplementationsDeps(ctx android.BazelConversionPathContext, exportsDeps bool, allDeps, exportedDeps []string, fn bazelLabelForDepsFn) depsPartition {
	if !exportsDeps {
		return depsPartition{
			implementation: fn(ctx, allDeps),
		}
	}

	implementation, export := android.FilterList(allDeps, exportedDeps)

	return depsPartition{
		export:         fn(ctx, export),
		implementation: fn(ctx, implementation),
	}
}

type bazelLabelForDepsExcludesFn func(android.BazelConversionPathContext, []string, []string) bazel.LabelList

func maybePartitionExportedAndImplementationsDepsExcludes(ctx android.BazelConversionPathContext, exportsDeps bool, allDeps, excludes, exportedDeps []string, fn bazelLabelForDepsExcludesFn) depsPartition {
	if !exportsDeps {
		return depsPartition{
			implementation: fn(ctx, allDeps, excludes),
		}
	}
	implementation, export := android.FilterList(allDeps, exportedDeps)

	return depsPartition{
		export:         fn(ctx, export, excludes),
		implementation: fn(ctx, implementation, excludes),
	}
}

// Parses properties common to static and shared libraries. Also used for prebuilt libraries.
func bp2buildParseStaticOrSharedProps(ctx android.BazelConversionPathContext, module *Module, lib *libraryDecorator, isStatic bool) staticOrSharedAttributes {
	attrs := staticOrSharedAttributes{}

	setAttrs := func(axis bazel.ConfigurationAxis, config string, props StaticOrSharedProperties) {
		attrs.Copts.SetSelectValue(axis, config, parseCommandLineFlags(props.Cflags, true, filterOutStdFlag))
		attrs.Srcs.SetSelectValue(axis, config, android.BazelLabelForModuleSrc(ctx, props.Srcs))
		attrs.System_dynamic_deps.SetSelectValue(axis, config, bazelLabelForSharedDeps(ctx, props.System_shared_libs))

		staticDeps := maybePartitionExportedAndImplementationsDeps(ctx, true, props.Static_libs, props.Export_static_lib_headers, bazelLabelForStaticDeps)
		attrs.Deps.SetSelectValue(axis, config, staticDeps.export)
		attrs.Implementation_deps.SetSelectValue(axis, config, staticDeps.implementation)

		sharedDeps := maybePartitionExportedAndImplementationsDeps(ctx, true, props.Shared_libs, props.Export_shared_lib_headers, bazelLabelForSharedDeps)
		attrs.Dynamic_deps.SetSelectValue(axis, config, sharedDeps.export)
		attrs.Implementation_dynamic_deps.SetSelectValue(axis, config, sharedDeps.implementation)

		attrs.Whole_archive_deps.SetSelectValue(axis, config, bazelLabelForWholeDeps(ctx, props.Whole_static_libs))
		attrs.Enabled.SetSelectValue(axis, config, props.Enabled)
	}
	// system_dynamic_deps distinguishes between nil/empty list behavior:
	//    nil -> use default values
	//    empty list -> no values specified
	attrs.System_dynamic_deps.ForceSpecifyEmptyList = true

	if isStatic {
		bp2BuildPropParseHelper(ctx, module, &StaticProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
			if staticOrSharedProps, ok := props.(*StaticProperties); ok {
				setAttrs(axis, config, staticOrSharedProps.Static)
			}
		})
	} else {
		bp2BuildPropParseHelper(ctx, module, &SharedProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
			if staticOrSharedProps, ok := props.(*SharedProperties); ok {
				setAttrs(axis, config, staticOrSharedProps.Shared)
			}
		})
	}

	partitionedSrcs := groupSrcsByExtension(ctx, attrs.Srcs)
	attrs.Srcs = partitionedSrcs[cppSrcPartition]
	attrs.Srcs_c = partitionedSrcs[cSrcPartition]
	attrs.Srcs_as = partitionedSrcs[asSrcPartition]

	if !partitionedSrcs[protoSrcPartition].IsEmpty() {
		// TODO(b/208815215): determine whether this is used and add support if necessary
		ctx.ModuleErrorf("Migrating static/shared only proto srcs is not currently supported")
	}

	return attrs
}

// Convenience struct to hold all attributes parsed from prebuilt properties.
type prebuiltAttributes struct {
	Src     bazel.LabelAttribute
	Enabled bazel.BoolAttribute
}

// NOTE: Used outside of Soong repo project, in the clangprebuilts.go bootstrap_go_package
func Bp2BuildParsePrebuiltLibraryProps(ctx android.BazelConversionPathContext, module *Module, isStatic bool) prebuiltAttributes {
	manySourceFileError := func(axis bazel.ConfigurationAxis, config string) {
		ctx.ModuleErrorf("Bp2BuildParsePrebuiltLibraryProps: Expected at most one source file for %s %s\n", axis, config)
	}
	var srcLabelAttribute bazel.LabelAttribute

	parseSrcs := func(ctx android.BazelConversionPathContext, axis bazel.ConfigurationAxis, config string, srcs []string) {
		if len(srcs) > 1 {
			manySourceFileError(axis, config)
			return
		} else if len(srcs) == 0 {
			return
		}
		if srcLabelAttribute.SelectValue(axis, config) != nil {
			manySourceFileError(axis, config)
			return
		}

		src := android.BazelLabelForModuleSrcSingle(ctx, srcs[0])
		srcLabelAttribute.SetSelectValue(axis, config, src)
	}

	bp2BuildPropParseHelper(ctx, module, &prebuiltLinkerProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		if prebuiltLinkerProperties, ok := props.(*prebuiltLinkerProperties); ok {
			parseSrcs(ctx, axis, config, prebuiltLinkerProperties.Srcs)
		}
	})

	var enabledLabelAttribute bazel.BoolAttribute
	parseAttrs := func(axis bazel.ConfigurationAxis, config string, props StaticOrSharedProperties) {
		if props.Enabled != nil {
			enabledLabelAttribute.SetSelectValue(axis, config, props.Enabled)
		}
		parseSrcs(ctx, axis, config, props.Srcs)
	}

	if isStatic {
		bp2BuildPropParseHelper(ctx, module, &StaticProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
			if staticProperties, ok := props.(*StaticProperties); ok {
				parseAttrs(axis, config, staticProperties.Static)
			}
		})
	} else {
		bp2BuildPropParseHelper(ctx, module, &SharedProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
			if sharedProperties, ok := props.(*SharedProperties); ok {
				parseAttrs(axis, config, sharedProperties.Shared)
			}
		})
	}

	return prebuiltAttributes{
		Src:     srcLabelAttribute,
		Enabled: enabledLabelAttribute,
	}
}

func bp2BuildPropParseHelper(ctx android.ArchVariantContext, module *Module, propsType interface{}, parseFunc func(axis bazel.ConfigurationAxis, config string, props interface{})) {
	for axis, configToProps := range module.GetArchVariantProperties(ctx, propsType) {
		for config, props := range configToProps {
			parseFunc(axis, config, props)
		}
	}
}

type baseAttributes struct {
	compilerAttributes
	linkerAttributes

	// A combination of compilerAttributes.features and linkerAttributes.features
	features        bazel.StringListAttribute
	protoDependency *bazel.LabelAttribute
	aidlDependency  *bazel.LabelAttribute
}

// Convenience struct to hold all attributes parsed from compiler properties.
type compilerAttributes struct {
	// Options for all languages
	copts bazel.StringListAttribute
	// Assembly options and sources
	asFlags bazel.StringListAttribute
	asSrcs  bazel.LabelListAttribute
	asmSrcs bazel.LabelListAttribute
	// C options and sources
	conlyFlags bazel.StringListAttribute
	cSrcs      bazel.LabelListAttribute
	// C++ options and sources
	cppFlags bazel.StringListAttribute
	srcs     bazel.LabelListAttribute

	// Lex sources and options
	lSrcs   bazel.LabelListAttribute
	llSrcs  bazel.LabelListAttribute
	lexopts bazel.StringListAttribute

	hdrs bazel.LabelListAttribute

	rtti bazel.BoolAttribute

	// Not affected by arch variants
	stl    *string
	cStd   *string
	cppStd *string

	localIncludes    bazel.StringListAttribute
	absoluteIncludes bazel.StringListAttribute

	includes BazelIncludes

	protoSrcs bazel.LabelListAttribute
	aidlSrcs  bazel.LabelListAttribute

	stubsSymbolFile *string
	stubsVersions   bazel.StringListAttribute

	features bazel.StringListAttribute

	suffix bazel.StringAttribute
}

type filterOutFn func(string) bool

func filterOutStdFlag(flag string) bool {
	return strings.HasPrefix(flag, "-std=")
}

func filterOutClangUnknownCflags(flag string) bool {
	for _, f := range config.ClangUnknownCflags {
		if f == flag {
			return true
		}
	}
	return false
}

func parseCommandLineFlags(soongFlags []string, noCoptsTokenization bool, filterOut ...filterOutFn) []string {
	var result []string
	for _, flag := range soongFlags {
		skipFlag := false
		for _, filter := range filterOut {
			if filter != nil && filter(flag) {
				skipFlag = true
			}
		}
		if skipFlag {
			continue
		}
		// Soong's cflags can contain spaces, like `-include header.h`. For
		// Bazel's copts, split them up to be compatible with the
		// no_copts_tokenization feature.
		if noCoptsTokenization {
			result = append(result, strings.Split(flag, " ")...)
		} else {
			// Soong's Version Script and Dynamic List Properties are added as flags
			// to Bazel's linkopts using "($location label)" syntax.
			// Splitting on spaces would separate this into two different flags
			// "($ location" and "label)"
			result = append(result, flag)
		}
	}
	return result
}

func (ca *compilerAttributes) bp2buildForAxisAndConfig(ctx android.BazelConversionPathContext, axis bazel.ConfigurationAxis, config string, props *BaseCompilerProperties) {
	// If there's arch specific srcs or exclude_srcs, generate a select entry for it.
	// TODO(b/186153868): do this for OS specific srcs and exclude_srcs too.
	if srcsList, ok := parseSrcs(ctx, props); ok {
		ca.srcs.SetSelectValue(axis, config, srcsList)
	}

	localIncludeDirs := props.Local_include_dirs
	if axis == bazel.NoConfigAxis {
		ca.cStd, ca.cppStd = bp2buildResolveCppStdValue(props.C_std, props.Cpp_std, props.Gnu_extensions)
		if includeBuildDirectory(props.Include_build_directory) {
			localIncludeDirs = append(localIncludeDirs, ".")
		}
	}

	ca.absoluteIncludes.SetSelectValue(axis, config, props.Include_dirs)
	ca.localIncludes.SetSelectValue(axis, config, localIncludeDirs)

	instructionSet := proptools.StringDefault(props.Instruction_set, "")
	if instructionSet == "arm" {
		ca.features.SetSelectValue(axis, config, []string{"arm_isa_arm", "-arm_isa_thumb"})
	} else if instructionSet != "" && instructionSet != "thumb" {
		ctx.ModuleErrorf("Unknown value for instruction_set: %s", instructionSet)
	}

	// In Soong, cflags occur on the command line before -std=<val> flag, resulting in the value being
	// overridden. In Bazel we always allow overriding, via flags; however, this can cause
	// incompatibilities, so we remove "-std=" flags from Cflag properties while leaving it in other
	// cases.
	ca.copts.SetSelectValue(axis, config, parseCommandLineFlags(props.Cflags, true, filterOutStdFlag, filterOutClangUnknownCflags))
	ca.asFlags.SetSelectValue(axis, config, parseCommandLineFlags(props.Asflags, true, nil))
	ca.conlyFlags.SetSelectValue(axis, config, parseCommandLineFlags(props.Conlyflags, true, filterOutClangUnknownCflags))
	ca.cppFlags.SetSelectValue(axis, config, parseCommandLineFlags(props.Cppflags, true, filterOutClangUnknownCflags))
	ca.rtti.SetSelectValue(axis, config, props.Rtti)
}

func (ca *compilerAttributes) convertStlProps(ctx android.ArchVariantContext, module *Module) {
	bp2BuildPropParseHelper(ctx, module, &StlProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		if stlProps, ok := props.(*StlProperties); ok {
			if stlProps.Stl == nil {
				return
			}
			if ca.stl == nil {
				stl := deduplicateStlInput(*stlProps.Stl)
				ca.stl = &stl
			} else if ca.stl != stlProps.Stl {
				ctx.ModuleErrorf("Unsupported conversion: module with different stl for different variants: %s and %s", *ca.stl, stlProps.Stl)
			}
		}
	})
}

func (ca *compilerAttributes) convertProductVariables(ctx android.BazelConversionPathContext, productVariableProps android.ProductConfigProperties) {
	productVarPropNameToAttribute := map[string]*bazel.StringListAttribute{
		"Cflags":   &ca.copts,
		"Asflags":  &ca.asFlags,
		"CppFlags": &ca.cppFlags,
	}
	for propName, attr := range productVarPropNameToAttribute {
		if productConfigProps, exists := productVariableProps[propName]; exists {
			for productConfigProp, prop := range productConfigProps {
				flags, ok := prop.([]string)
				if !ok {
					ctx.ModuleErrorf("Could not convert product variable %s property", proptools.PropertyNameForField(propName))
				}
				newFlags, _ := bazel.TryVariableSubstitutions(flags, productConfigProp.Name)
				attr.SetSelectValue(productConfigProp.ConfigurationAxis(), productConfigProp.SelectKey(), newFlags)
			}
		}
	}
}

func (ca *compilerAttributes) finalize(ctx android.BazelConversionPathContext, implementationHdrs bazel.LabelListAttribute) {
	ca.srcs.ResolveExcludes()
	partitionedSrcs := groupSrcsByExtension(ctx, ca.srcs)

	ca.protoSrcs = partitionedSrcs[protoSrcPartition]
	ca.aidlSrcs = partitionedSrcs[aidlSrcPartition]

	for p, lla := range partitionedSrcs {
		// if there are no sources, there is no need for headers
		if lla.IsEmpty() {
			continue
		}
		lla.Append(implementationHdrs)
		partitionedSrcs[p] = lla
	}

	ca.srcs = partitionedSrcs[cppSrcPartition]
	ca.cSrcs = partitionedSrcs[cSrcPartition]
	ca.asSrcs = partitionedSrcs[asSrcPartition]
	ca.asmSrcs = partitionedSrcs[asmSrcPartition]
	ca.lSrcs = partitionedSrcs[lSrcPartition]
	ca.llSrcs = partitionedSrcs[llSrcPartition]

	ca.absoluteIncludes.DeduplicateAxesFromBase()
	ca.localIncludes.DeduplicateAxesFromBase()
}

// Parse srcs from an arch or OS's props value.
func parseSrcs(ctx android.BazelConversionPathContext, props *BaseCompilerProperties) (bazel.LabelList, bool) {
	anySrcs := false
	// Add srcs-like dependencies such as generated files.
	// First create a LabelList containing these dependencies, then merge the values with srcs.
	generatedSrcsLabelList := android.BazelLabelForModuleDepsExcludes(ctx, props.Generated_sources, props.Exclude_generated_sources)
	if len(props.Generated_sources) > 0 || len(props.Exclude_generated_sources) > 0 {
		anySrcs = true
	}

	allSrcsLabelList := android.BazelLabelForModuleSrcExcludes(ctx, props.Srcs, props.Exclude_srcs)

	if len(props.Srcs) > 0 || len(props.Exclude_srcs) > 0 {
		anySrcs = true
	}

	return bazel.AppendBazelLabelLists(allSrcsLabelList, generatedSrcsLabelList), anySrcs
}

// Given a name in srcs prop, check to see if the name references a filegroup
// and the filegroup is converted to aidl_library
func isConvertedToAidlLibrary(ctx android.BazelConversionPathContext, name string) bool {
	if module, ok := ctx.ModuleFromName(name); ok {
		if android.IsFilegroup(ctx, module) {
			if fg, ok := module.(android.Bp2buildAidlLibrary); ok {
				return fg.ShouldConvertToAidlLibrary(ctx)
			}
		}
	}
	return false
}

func bp2buildStdVal(std *string, prefix string, useGnu bool) *string {
	defaultVal := prefix + "_std_default"
	// If c{,pp}std properties are not specified, don't generate them in the BUILD file.
	// Defaults are handled by the toolchain definition.
	// However, if gnu_extensions is false, then the default gnu-to-c version must be specified.
	stdVal := proptools.StringDefault(std, defaultVal)
	if stdVal == "experimental" || stdVal == defaultVal {
		if stdVal == "experimental" {
			stdVal = prefix + "_std_experimental"
		}
		if !useGnu {
			stdVal += "_no_gnu"
		}
	} else if !useGnu {
		stdVal = gnuToCReplacer.Replace(stdVal)
	}

	if stdVal == defaultVal {
		return nil
	}
	return &stdVal
}

func bp2buildResolveCppStdValue(c_std *string, cpp_std *string, gnu_extensions *bool) (*string, *string) {
	useGnu := useGnuExtensions(gnu_extensions)

	return bp2buildStdVal(c_std, "c", useGnu), bp2buildStdVal(cpp_std, "cpp", useGnu)
}

// packageFromLabel extracts package from a fully-qualified or relative Label and whether the label
// is fully-qualified.
// e.g. fully-qualified "//a/b:foo" -> "a/b", true, relative: ":bar" -> ".", false
func packageFromLabel(label string) (string, bool) {
	split := strings.Split(label, ":")
	if len(split) != 2 {
		return "", false
	}
	if split[0] == "" {
		return ".", false
	}
	// remove leading "//"
	return split[0][2:], true
}

// includesFromLabelList extracts relative/absolute includes from a bazel.LabelList>
func includesFromLabelList(labelList bazel.LabelList) (relative, absolute []string) {
	for _, hdr := range labelList.Includes {
		if pkg, hasPkg := packageFromLabel(hdr.Label); hasPkg {
			absolute = append(absolute, pkg)
		} else if pkg != "" {
			relative = append(relative, pkg)
		}
	}
	return relative, absolute
}

type YasmAttributes struct {
	Srcs         bazel.LabelListAttribute
	Flags        bazel.StringListAttribute
	Include_dirs bazel.StringListAttribute
}

func bp2BuildYasm(ctx android.Bp2buildMutatorContext, m *Module, ca compilerAttributes) *bazel.LabelAttribute {
	if ca.asmSrcs.IsEmpty() {
		return nil
	}

	// Yasm needs the include directories from both local_includes and
	// export_include_dirs. We don't care about actually exporting them from the
	// yasm rule though, because they will also be present on the cc_ rule that
	// wraps this yasm rule.
	includes := ca.localIncludes.Clone()
	bp2BuildPropParseHelper(ctx, m, &FlagExporterProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		if flagExporterProperties, ok := props.(*FlagExporterProperties); ok {
			if len(flagExporterProperties.Export_include_dirs) > 0 {
				x := bazel.StringListAttribute{}
				x.SetSelectValue(axis, config, flagExporterProperties.Export_include_dirs)
				includes.Append(x)
			}
		}
	})

	ctx.CreateBazelTargetModule(
		bazel.BazelTargetModuleProperties{
			Rule_class:        "yasm",
			Bzl_load_location: "//build/bazel/rules/cc:yasm.bzl",
		},
		android.CommonAttributes{Name: m.Name() + "_yasm"},
		&YasmAttributes{
			Srcs:         ca.asmSrcs,
			Flags:        ca.asFlags,
			Include_dirs: *includes,
		})

	// We only want to add a dependency on the _yasm target if there are asm
	// sources in the current configuration. If there are unconfigured asm
	// sources, always add the dependency. Otherwise, add the dependency only
	// on the configuration axes and values that had asm sources.
	if len(ca.asmSrcs.Value.Includes) > 0 {
		return bazel.MakeLabelAttribute(":" + m.Name() + "_yasm")
	}

	ret := &bazel.LabelAttribute{}
	for _, axis := range ca.asmSrcs.SortedConfigurationAxes() {
		for config := range ca.asmSrcs.ConfigurableValues[axis] {
			ret.SetSelectValue(axis, config, bazel.Label{Label: ":" + m.Name() + "_yasm"})
		}
	}
	return ret
}

// bp2BuildParseBaseProps returns all compiler, linker, library attributes of a cc module..
func bp2BuildParseBaseProps(ctx android.Bp2buildMutatorContext, module *Module) baseAttributes {
	archVariantCompilerProps := module.GetArchVariantProperties(ctx, &BaseCompilerProperties{})
	archVariantLinkerProps := module.GetArchVariantProperties(ctx, &BaseLinkerProperties{})
	archVariantLibraryProperties := module.GetArchVariantProperties(ctx, &LibraryProperties{})

	var implementationHdrs bazel.LabelListAttribute

	axisToConfigs := map[bazel.ConfigurationAxis]map[string]bool{}
	allAxesAndConfigs := func(cp android.ConfigurationAxisToArchVariantProperties) {
		for axis, configMap := range cp {
			if _, ok := axisToConfigs[axis]; !ok {
				axisToConfigs[axis] = map[string]bool{}
			}
			for config, _ := range configMap {
				axisToConfigs[axis][config] = true
			}
		}
	}
	allAxesAndConfigs(archVariantCompilerProps)
	allAxesAndConfigs(archVariantLinkerProps)
	allAxesAndConfigs(archVariantLibraryProperties)

	compilerAttrs := compilerAttributes{}
	linkerAttrs := linkerAttributes{}

	for axis, configs := range axisToConfigs {
		for config, _ := range configs {
			var allHdrs []string
			if baseCompilerProps, ok := archVariantCompilerProps[axis][config].(*BaseCompilerProperties); ok {
				allHdrs = baseCompilerProps.Generated_headers
				if baseCompilerProps.Lex != nil {
					compilerAttrs.lexopts.SetSelectValue(axis, config, baseCompilerProps.Lex.Flags)
				}
				(&compilerAttrs).bp2buildForAxisAndConfig(ctx, axis, config, baseCompilerProps)
			}

			var exportHdrs []string

			if baseLinkerProps, ok := archVariantLinkerProps[axis][config].(*BaseLinkerProperties); ok {
				exportHdrs = baseLinkerProps.Export_generated_headers

				(&linkerAttrs).bp2buildForAxisAndConfig(ctx, module.Binary(), axis, config, baseLinkerProps)
			}
			headers := maybePartitionExportedAndImplementationsDeps(ctx, !module.Binary(), allHdrs, exportHdrs, android.BazelLabelForModuleDeps)
			implementationHdrs.SetSelectValue(axis, config, headers.implementation)
			compilerAttrs.hdrs.SetSelectValue(axis, config, headers.export)

			exportIncludes, exportAbsoluteIncludes := includesFromLabelList(headers.export)
			compilerAttrs.includes.Includes.SetSelectValue(axis, config, exportIncludes)
			compilerAttrs.includes.AbsoluteIncludes.SetSelectValue(axis, config, exportAbsoluteIncludes)

			includes, absoluteIncludes := includesFromLabelList(headers.implementation)
			currAbsoluteIncludes := compilerAttrs.absoluteIncludes.SelectValue(axis, config)
			currAbsoluteIncludes = android.FirstUniqueStrings(append(currAbsoluteIncludes, absoluteIncludes...))

			compilerAttrs.absoluteIncludes.SetSelectValue(axis, config, currAbsoluteIncludes)

			currIncludes := compilerAttrs.localIncludes.SelectValue(axis, config)
			currIncludes = android.FirstUniqueStrings(append(currIncludes, includes...))

			compilerAttrs.localIncludes.SetSelectValue(axis, config, currIncludes)

			if libraryProps, ok := archVariantLibraryProperties[axis][config].(*LibraryProperties); ok {
				if axis == bazel.NoConfigAxis {
					compilerAttrs.stubsSymbolFile = libraryProps.Stubs.Symbol_file
					compilerAttrs.stubsVersions.SetSelectValue(axis, config, libraryProps.Stubs.Versions)
				}
				if suffix := libraryProps.Suffix; suffix != nil {
					compilerAttrs.suffix.SetSelectValue(axis, config, suffix)
				}
			}
		}
	}

	compilerAttrs.convertStlProps(ctx, module)
	(&linkerAttrs).convertStripProps(ctx, module)

	if module.coverage != nil && module.coverage.Properties.Native_coverage != nil &&
		!Bool(module.coverage.Properties.Native_coverage) {
		// Native_coverage is arch neutral
		(&linkerAttrs).features.Append(bazel.MakeStringListAttribute([]string{"-coverage"}))
	}

	productVariableProps := android.ProductVariableProperties(ctx)

	(&compilerAttrs).convertProductVariables(ctx, productVariableProps)
	(&linkerAttrs).convertProductVariables(ctx, productVariableProps)

	(&compilerAttrs).finalize(ctx, implementationHdrs)
	(&linkerAttrs).finalize(ctx)

	(&compilerAttrs.srcs).Add(bp2BuildYasm(ctx, module, compilerAttrs))

	protoDep := bp2buildProto(ctx, module, compilerAttrs.protoSrcs)

	// bp2buildProto will only set wholeStaticLib or implementationWholeStaticLib, but we don't know
	// which. This will add the newly generated proto library to the appropriate attribute and nothing
	// to the other
	(&linkerAttrs).wholeArchiveDeps.Add(protoDep.wholeStaticLib)
	(&linkerAttrs).implementationWholeArchiveDeps.Add(protoDep.implementationWholeStaticLib)

	aidlDep := bp2buildCcAidlLibrary(ctx, module, compilerAttrs.aidlSrcs)
	if aidlDep != nil {
		if lib, ok := module.linker.(*libraryDecorator); ok {
			if proptools.Bool(lib.Properties.Aidl.Export_aidl_headers) {
				(&linkerAttrs).wholeArchiveDeps.Add(aidlDep)
			} else {
				(&linkerAttrs).implementationWholeArchiveDeps.Add(aidlDep)
			}
		}
	}

	convertedLSrcs := bp2BuildLex(ctx, module.Name(), compilerAttrs)
	(&compilerAttrs).srcs.Add(&convertedLSrcs.srcName)
	(&compilerAttrs).cSrcs.Add(&convertedLSrcs.cSrcName)

	features := compilerAttrs.features.Clone().Append(linkerAttrs.features)
	features.DeduplicateAxesFromBase()

	return baseAttributes{
		compilerAttrs,
		linkerAttrs,
		*features,
		protoDep.protoDep,
		aidlDep,
	}
}

func bp2buildAidlLibraries(
	ctx android.Bp2buildMutatorContext,
	m *Module,
	aidlSrcs bazel.LabelListAttribute,
) bazel.LabelList {
	var aidlLibraries bazel.LabelList
	var directAidlSrcs bazel.LabelList

	// Make a list of labels that correspond to filegroups that are already converted to aidl_library
	for _, aidlSrc := range aidlSrcs.Value.Includes {
		src := aidlSrc.OriginalModuleName
		if isConvertedToAidlLibrary(ctx, src) {
			module, _ := ctx.ModuleFromName(src)
			fg, _ := module.(android.Bp2buildAidlLibrary)
			aidlLibraries.Add(&bazel.Label{
				Label: fg.GetAidlLibraryLabel(ctx),
			})
		} else {
			directAidlSrcs.Add(&aidlSrc)
		}
	}

	if len(directAidlSrcs.Includes) > 0 {
		aidlLibraryLabel := m.Name() + "_aidl_library"
		ctx.CreateBazelTargetModule(
			bazel.BazelTargetModuleProperties{
				Rule_class:        "aidl_library",
				Bzl_load_location: "//build/bazel/rules/aidl:library.bzl",
			},
			android.CommonAttributes{Name: aidlLibraryLabel},
			&aidlLibraryAttributes{
				Srcs: bazel.MakeLabelListAttribute(directAidlSrcs),
			},
		)
		aidlLibraries.Add(&bazel.Label{
			Label: ":" + aidlLibraryLabel,
		})
	}
	return aidlLibraries
}

func bp2buildCcAidlLibrary(
	ctx android.Bp2buildMutatorContext,
	m *Module,
	aidlSrcs bazel.LabelListAttribute,
) *bazel.LabelAttribute {
	suffix := "_cc_aidl_library"
	ccAidlLibrarylabel := m.Name() + suffix

	aidlLibraries := bp2buildAidlLibraries(ctx, m, aidlSrcs)

	if aidlLibraries.IsEmpty() {
		return nil
	}

	ctx.CreateBazelTargetModule(
		bazel.BazelTargetModuleProperties{
			Rule_class:        "cc_aidl_library",
			Bzl_load_location: "//build/bazel/rules/cc:cc_aidl_library.bzl",
		},
		android.CommonAttributes{Name: ccAidlLibrarylabel},
		&ccAidlLibraryAttributes{
			Deps: bazel.MakeLabelListAttribute(aidlLibraries),
		},
	)

	label := &bazel.LabelAttribute{
		Value: &bazel.Label{
			Label: ":" + ccAidlLibrarylabel,
		},
	}
	return label
}

func bp2BuildParseSdkAttributes(module *Module) sdkAttributes {
	return sdkAttributes{
		Sdk_version:     module.Properties.Sdk_version,
		Min_sdk_version: module.Properties.Min_sdk_version,
	}
}

type sdkAttributes struct {
	Sdk_version     *string
	Min_sdk_version *string
}

// Convenience struct to hold all attributes parsed from linker properties.
type linkerAttributes struct {
	deps                             bazel.LabelListAttribute
	implementationDeps               bazel.LabelListAttribute
	dynamicDeps                      bazel.LabelListAttribute
	implementationDynamicDeps        bazel.LabelListAttribute
	runtimeDeps                      bazel.LabelListAttribute
	wholeArchiveDeps                 bazel.LabelListAttribute
	implementationWholeArchiveDeps   bazel.LabelListAttribute
	systemDynamicDeps                bazel.LabelListAttribute
	usedSystemDynamicDepAsDynamicDep map[string]bool

	linkCrt                       bazel.BoolAttribute
	useLibcrt                     bazel.BoolAttribute
	useVersionLib                 bazel.BoolAttribute
	linkopts                      bazel.StringListAttribute
	additionalLinkerInputs        bazel.LabelListAttribute
	stripKeepSymbols              bazel.BoolAttribute
	stripKeepSymbolsAndDebugFrame bazel.BoolAttribute
	stripKeepSymbolsList          bazel.StringListAttribute
	stripAll                      bazel.BoolAttribute
	stripNone                     bazel.BoolAttribute
	features                      bazel.StringListAttribute
}

var (
	soongSystemSharedLibs = []string{"libc", "libm", "libdl"}
)

func (la *linkerAttributes) bp2buildForAxisAndConfig(ctx android.BazelConversionPathContext, isBinary bool, axis bazel.ConfigurationAxis, config string, props *BaseLinkerProperties) {
	// Use a single variable to capture usage of nocrt in arch variants, so there's only 1 error message for this module
	var axisFeatures []string

	wholeStaticLibs := android.FirstUniqueStrings(props.Whole_static_libs)
	la.wholeArchiveDeps.SetSelectValue(axis, config, bazelLabelForWholeDepsExcludes(ctx, wholeStaticLibs, props.Exclude_static_libs))
	// Excludes to parallel Soong:
	// https://cs.android.com/android/platform/superproject/+/master:build/soong/cc/linker.go;l=247-249;drc=088b53577dde6e40085ffd737a1ae96ad82fc4b0
	staticLibs := android.FirstUniqueStrings(android.RemoveListFromList(props.Static_libs, wholeStaticLibs))

	staticDeps := maybePartitionExportedAndImplementationsDepsExcludes(ctx, !isBinary, staticLibs, props.Exclude_static_libs, props.Export_static_lib_headers, bazelLabelForStaticDepsExcludes)

	headerLibs := android.FirstUniqueStrings(props.Header_libs)
	hDeps := maybePartitionExportedAndImplementationsDeps(ctx, !isBinary, headerLibs, props.Export_header_lib_headers, bazelLabelForHeaderDeps)

	(&hDeps.export).Append(staticDeps.export)
	la.deps.SetSelectValue(axis, config, hDeps.export)

	(&hDeps.implementation).Append(staticDeps.implementation)
	la.implementationDeps.SetSelectValue(axis, config, hDeps.implementation)

	systemSharedLibs := props.System_shared_libs
	// systemSharedLibs distinguishes between nil/empty list behavior:
	//    nil -> use default values
	//    empty list -> no values specified
	if len(systemSharedLibs) > 0 {
		systemSharedLibs = android.FirstUniqueStrings(systemSharedLibs)
	}
	la.systemDynamicDeps.SetSelectValue(axis, config, bazelLabelForSharedDeps(ctx, systemSharedLibs))

	sharedLibs := android.FirstUniqueStrings(props.Shared_libs)
	excludeSharedLibs := props.Exclude_shared_libs
	usedSystem := android.FilterListPred(sharedLibs, func(s string) bool {
		return android.InList(s, soongSystemSharedLibs) && !android.InList(s, excludeSharedLibs)
	})
	for _, el := range usedSystem {
		if la.usedSystemDynamicDepAsDynamicDep == nil {
			la.usedSystemDynamicDepAsDynamicDep = map[string]bool{}
		}
		la.usedSystemDynamicDepAsDynamicDep[el] = true
	}

	sharedDeps := maybePartitionExportedAndImplementationsDepsExcludes(ctx, !isBinary, sharedLibs, props.Exclude_shared_libs, props.Export_shared_lib_headers, bazelLabelForSharedDepsExcludes)
	la.dynamicDeps.SetSelectValue(axis, config, sharedDeps.export)
	la.implementationDynamicDeps.SetSelectValue(axis, config, sharedDeps.implementation)
	if axis == bazel.NoConfigAxis || (axis == bazel.OsConfigurationAxis && config == bazel.OsAndroid) {
		// If a dependency in la.implementationDynamicDeps has stubs, its stub variant should be
		// used when the dependency is linked in a APEX. The dependencies in NoConfigAxis and
		// OsConfigurationAxis/OsAndroid are grouped by having stubs or not, so Bazel select()
		// statement can be used to choose source/stub variants of them.
		depsWithStubs := []bazel.Label{}
		for _, l := range sharedDeps.implementation.Includes {
			dep, _ := ctx.ModuleFromName(l.OriginalModuleName)
			if m, ok := dep.(*Module); ok && m.HasStubsVariants() {
				depsWithStubs = append(depsWithStubs, l)
			}
		}
		if len(depsWithStubs) > 0 {
			implDynamicDeps := bazel.SubtractBazelLabelList(sharedDeps.implementation, bazel.MakeLabelList(depsWithStubs))
			la.implementationDynamicDeps.SetSelectValue(axis, config, implDynamicDeps)

			stubLibLabels := []bazel.Label{}
			for _, l := range depsWithStubs {
				l.Label = l.Label + "_stub_libs_current"
				stubLibLabels = append(stubLibLabels, l)
			}
			inApexSelectValue := la.implementationDynamicDeps.SelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndInApex)
			nonApexSelectValue := la.implementationDynamicDeps.SelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndNonApex)
			defaultSelectValue := la.implementationDynamicDeps.SelectValue(bazel.OsAndInApexAxis, bazel.ConditionsDefaultConfigKey)
			if axis == bazel.NoConfigAxis {
				(&inApexSelectValue).Append(bazel.MakeLabelList(stubLibLabels))
				(&nonApexSelectValue).Append(bazel.MakeLabelList(depsWithStubs))
				(&defaultSelectValue).Append(bazel.MakeLabelList(depsWithStubs))
				la.implementationDynamicDeps.SetSelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndInApex, inApexSelectValue)
				la.implementationDynamicDeps.SetSelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndNonApex, nonApexSelectValue)
				la.implementationDynamicDeps.SetSelectValue(bazel.OsAndInApexAxis, bazel.ConditionsDefaultConfigKey, defaultSelectValue)
			} else if config == bazel.OsAndroid {
				(&inApexSelectValue).Append(bazel.MakeLabelList(stubLibLabels))
				(&nonApexSelectValue).Append(bazel.MakeLabelList(depsWithStubs))
				la.implementationDynamicDeps.SetSelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndInApex, inApexSelectValue)
				la.implementationDynamicDeps.SetSelectValue(bazel.OsAndInApexAxis, bazel.AndroidAndNonApex, nonApexSelectValue)
			}
		}
	}

	if !BoolDefault(props.Pack_relocations, packRelocationsDefault) {
		axisFeatures = append(axisFeatures, "disable_pack_relocations")
	}

	if Bool(props.Allow_undefined_symbols) {
		axisFeatures = append(axisFeatures, "-no_undefined_symbols")
	}

	var linkerFlags []string
	if len(props.Ldflags) > 0 {
		linkerFlags = append(linkerFlags, proptools.NinjaEscapeList(props.Ldflags)...)
		// binaries remove static flag if -shared is in the linker flags
		if isBinary && android.InList("-shared", linkerFlags) {
			axisFeatures = append(axisFeatures, "-static_flag")
		}
	}
	if props.Version_script != nil {
		label := android.BazelLabelForModuleSrcSingle(ctx, *props.Version_script)
		la.additionalLinkerInputs.SetSelectValue(axis, config, bazel.LabelList{Includes: []bazel.Label{label}})
		linkerFlags = append(linkerFlags, fmt.Sprintf("-Wl,--version-script,$(location %s)", label.Label))
	}

	if props.Dynamic_list != nil {
		label := android.BazelLabelForModuleSrcSingle(ctx, *props.Dynamic_list)
		la.additionalLinkerInputs.SetSelectValue(axis, config, bazel.LabelList{Includes: []bazel.Label{label}})
		linkerFlags = append(linkerFlags, fmt.Sprintf("-Wl,--dynamic-list,$(location %s)", label.Label))
	}

	la.linkopts.SetSelectValue(axis, config, parseCommandLineFlags(linkerFlags, false, filterOutClangUnknownCflags))
	la.useLibcrt.SetSelectValue(axis, config, props.libCrt())

	if axis == bazel.NoConfigAxis {
		la.useVersionLib.SetSelectValue(axis, config, props.Use_version_lib)
	}

	// it's very unlikely for nocrt to be arch variant, so bp2build doesn't support it.
	if props.crt() != nil {
		if axis == bazel.NoConfigAxis {
			la.linkCrt.SetSelectValue(axis, config, props.crt())
		} else if axis == bazel.ArchConfigurationAxis {
			ctx.ModuleErrorf("nocrt is not supported for arch variants")
		}
	}

	if axisFeatures != nil {
		la.features.SetSelectValue(axis, config, axisFeatures)
	}

	runtimeDeps := android.BazelLabelForModuleDepsExcludes(ctx, props.Runtime_libs, props.Exclude_runtime_libs)
	if !runtimeDeps.IsEmpty() {
		la.runtimeDeps.SetSelectValue(axis, config, runtimeDeps)
	}
}

func (la *linkerAttributes) convertStripProps(ctx android.BazelConversionPathContext, module *Module) {
	bp2BuildPropParseHelper(ctx, module, &StripProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		if stripProperties, ok := props.(*StripProperties); ok {
			la.stripKeepSymbols.SetSelectValue(axis, config, stripProperties.Strip.Keep_symbols)
			la.stripKeepSymbolsList.SetSelectValue(axis, config, stripProperties.Strip.Keep_symbols_list)
			la.stripKeepSymbolsAndDebugFrame.SetSelectValue(axis, config, stripProperties.Strip.Keep_symbols_and_debug_frame)
			la.stripAll.SetSelectValue(axis, config, stripProperties.Strip.All)
			la.stripNone.SetSelectValue(axis, config, stripProperties.Strip.None)
		}
	})
}

func (la *linkerAttributes) convertProductVariables(ctx android.BazelConversionPathContext, productVariableProps android.ProductConfigProperties) {

	type productVarDep struct {
		// the name of the corresponding excludes field, if one exists
		excludesField string
		// reference to the bazel attribute that should be set for the given product variable config
		attribute *bazel.LabelListAttribute

		depResolutionFunc func(ctx android.BazelConversionPathContext, modules, excludes []string) bazel.LabelList
	}

	// an intermediate attribute that holds Header_libs info, and will be appended to
	// implementationDeps at the end, to solve the confliction that both header_libs
	// and static_libs use implementationDeps.
	var headerDeps bazel.LabelListAttribute

	productVarToDepFields := map[string]productVarDep{
		// product variables do not support exclude_shared_libs
		"Shared_libs":       {attribute: &la.implementationDynamicDeps, depResolutionFunc: bazelLabelForSharedDepsExcludes},
		"Static_libs":       {"Exclude_static_libs", &la.implementationDeps, bazelLabelForStaticDepsExcludes},
		"Whole_static_libs": {"Exclude_static_libs", &la.wholeArchiveDeps, bazelLabelForWholeDepsExcludes},
		"Header_libs":       {attribute: &headerDeps, depResolutionFunc: bazelLabelForHeaderDepsExcludes},
	}

	for name, dep := range productVarToDepFields {
		props, exists := productVariableProps[name]
		excludeProps, excludesExists := productVariableProps[dep.excludesField]
		// if neither an include or excludes property exists, then skip it
		if !exists && !excludesExists {
			continue
		}
		// Collect all the configurations that an include or exclude property exists for.
		// We want to iterate all configurations rather than either the include or exclude because, for a
		// particular configuration, we may have either only an include or an exclude to handle.
		productConfigProps := make(map[android.ProductConfigProperty]bool, len(props)+len(excludeProps))
		for p := range props {
			productConfigProps[p] = true
		}
		for p := range excludeProps {
			productConfigProps[p] = true
		}

		for productConfigProp := range productConfigProps {
			prop, includesExists := props[productConfigProp]
			excludesProp, excludesExists := excludeProps[productConfigProp]
			var includes, excludes []string
			var ok bool
			// if there was no includes/excludes property, casting fails and that's expected
			if includes, ok = prop.([]string); includesExists && !ok {
				ctx.ModuleErrorf("Could not convert product variable %s property", name)
			}
			if excludes, ok = excludesProp.([]string); excludesExists && !ok {
				ctx.ModuleErrorf("Could not convert product variable %s property", dep.excludesField)
			}

			dep.attribute.EmitEmptyList = productConfigProp.AlwaysEmit()
			dep.attribute.SetSelectValue(
				productConfigProp.ConfigurationAxis(),
				productConfigProp.SelectKey(),
				dep.depResolutionFunc(ctx, android.FirstUniqueStrings(includes), excludes),
			)
		}
	}
	la.implementationDeps.Append(headerDeps)
}

func (la *linkerAttributes) finalize(ctx android.BazelConversionPathContext) {
	// if system dynamic deps have the default value, any use of a system dynamic library used will
	// result in duplicate library errors for bionic OSes. Here, we explicitly exclude those libraries
	// from bionic OSes and the no config case as these libraries only build for bionic OSes.
	if la.systemDynamicDeps.IsNil() && len(la.usedSystemDynamicDepAsDynamicDep) > 0 {
		toRemove := bazelLabelForSharedDeps(ctx, android.SortedStringKeys(la.usedSystemDynamicDepAsDynamicDep))
		la.dynamicDeps.Exclude(bazel.NoConfigAxis, "", toRemove)
		la.implementationDynamicDeps.Exclude(bazel.NoConfigAxis, "", toRemove)
		la.implementationDynamicDeps.Exclude(bazel.NoConfigAxis, "", toRemove)
		la.dynamicDeps.Exclude(bazel.OsConfigurationAxis, "android", toRemove)
		la.dynamicDeps.Exclude(bazel.OsConfigurationAxis, "linux_bionic", toRemove)
		la.implementationDynamicDeps.Exclude(bazel.OsConfigurationAxis, "android", toRemove)
		la.implementationDynamicDeps.Exclude(bazel.OsConfigurationAxis, "linux_bionic", toRemove)
	}

	la.deps.ResolveExcludes()
	la.implementationDeps.ResolveExcludes()
	la.dynamicDeps.ResolveExcludes()
	la.implementationDynamicDeps.ResolveExcludes()
	la.wholeArchiveDeps.ResolveExcludes()
	la.systemDynamicDeps.ForceSpecifyEmptyList = true

}

// Relativize a list of root-relative paths with respect to the module's
// directory.
//
// include_dirs Soong prop are root-relative (b/183742505), but
// local_include_dirs, export_include_dirs and export_system_include_dirs are
// module dir relative. This function makes a list of paths entirely module dir
// relative.
//
// For the `include` attribute, Bazel wants the paths to be relative to the
// module.
func bp2BuildMakePathsRelativeToModule(ctx android.BazelConversionPathContext, paths []string) []string {
	var relativePaths []string
	for _, path := range paths {
		// Semantics of filepath.Rel: join(ModuleDir, rel(ModuleDir, path)) == path
		relativePath, err := filepath.Rel(ctx.ModuleDir(), path)
		if err != nil {
			panic(err)
		}
		relativePaths = append(relativePaths, relativePath)
	}
	return relativePaths
}

// BazelIncludes contains information about -I and -isystem paths from a module converted to Bazel
// attributes.
type BazelIncludes struct {
	AbsoluteIncludes bazel.StringListAttribute
	Includes         bazel.StringListAttribute
	SystemIncludes   bazel.StringListAttribute
}

func bp2BuildParseExportedIncludes(ctx android.BazelConversionPathContext, module *Module, includes *BazelIncludes) BazelIncludes {
	var exported BazelIncludes
	if includes != nil {
		exported = *includes
	} else {
		exported = BazelIncludes{}
	}
	bp2BuildPropParseHelper(ctx, module, &FlagExporterProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		if flagExporterProperties, ok := props.(*FlagExporterProperties); ok {
			if len(flagExporterProperties.Export_include_dirs) > 0 {
				exported.Includes.SetSelectValue(axis, config, android.FirstUniqueStrings(append(exported.Includes.SelectValue(axis, config), flagExporterProperties.Export_include_dirs...)))
			}
			if len(flagExporterProperties.Export_system_include_dirs) > 0 {
				exported.SystemIncludes.SetSelectValue(axis, config, android.FirstUniqueStrings(append(exported.SystemIncludes.SelectValue(axis, config), flagExporterProperties.Export_system_include_dirs...)))
			}
		}
	})
	exported.AbsoluteIncludes.DeduplicateAxesFromBase()
	exported.Includes.DeduplicateAxesFromBase()
	exported.SystemIncludes.DeduplicateAxesFromBase()

	return exported
}

func bazelLabelForStaticModule(ctx android.BazelConversionPathContext, m blueprint.Module) string {
	label := android.BazelModuleLabel(ctx, m)
	if ccModule, ok := m.(*Module); ok && ccModule.typ() == fullLibrary && !android.GetBp2BuildAllowList().GenerateCcLibraryStaticOnly(m.Name()) {
		label += "_bp2build_cc_library_static"
	}
	return label
}

func bazelLabelForSharedModule(ctx android.BazelConversionPathContext, m blueprint.Module) string {
	// cc_library, at it's root name, propagates the shared library, which depends on the static
	// library.
	return android.BazelModuleLabel(ctx, m)
}

func bazelLabelForStaticWholeModuleDeps(ctx android.BazelConversionPathContext, m blueprint.Module) string {
	label := bazelLabelForStaticModule(ctx, m)
	if aModule, ok := m.(android.Module); ok {
		if android.IsModulePrebuilt(aModule) {
			label += "_alwayslink"
		}
	}
	return label
}

func bazelLabelForWholeDeps(ctx android.BazelConversionPathContext, modules []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsWithFn(ctx, modules, bazelLabelForStaticWholeModuleDeps)
}

func bazelLabelForWholeDepsExcludes(ctx android.BazelConversionPathContext, modules, excludes []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsExcludesWithFn(ctx, modules, excludes, bazelLabelForStaticWholeModuleDeps)
}

func bazelLabelForStaticDepsExcludes(ctx android.BazelConversionPathContext, modules, excludes []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsExcludesWithFn(ctx, modules, excludes, bazelLabelForStaticModule)
}

func bazelLabelForStaticDeps(ctx android.BazelConversionPathContext, modules []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsWithFn(ctx, modules, bazelLabelForStaticModule)
}

func bazelLabelForSharedDeps(ctx android.BazelConversionPathContext, modules []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsWithFn(ctx, modules, bazelLabelForSharedModule)
}

func bazelLabelForHeaderDeps(ctx android.BazelConversionPathContext, modules []string) bazel.LabelList {
	// This is not elegant, but bp2build's shared library targets only propagate
	// their header information as part of the normal C++ provider.
	return bazelLabelForSharedDeps(ctx, modules)
}

func bazelLabelForHeaderDepsExcludes(ctx android.BazelConversionPathContext, modules, excludes []string) bazel.LabelList {
	// This is only used when product_variable header_libs is processed, to follow
	// the pattern of depResolutionFunc
	return android.BazelLabelForModuleDepsExcludesWithFn(ctx, modules, excludes, bazelLabelForSharedModule)
}

func bazelLabelForSharedDepsExcludes(ctx android.BazelConversionPathContext, modules, excludes []string) bazel.LabelList {
	return android.BazelLabelForModuleDepsExcludesWithFn(ctx, modules, excludes, bazelLabelForSharedModule)
}

type binaryLinkerAttrs struct {
	Linkshared *bool
	Suffix     bazel.StringAttribute
}

func bp2buildBinaryLinkerProps(ctx android.BazelConversionPathContext, m *Module) binaryLinkerAttrs {
	attrs := binaryLinkerAttrs{}
	bp2BuildPropParseHelper(ctx, m, &BinaryLinkerProperties{}, func(axis bazel.ConfigurationAxis, config string, props interface{}) {
		linkerProps := props.(*BinaryLinkerProperties)
		staticExecutable := linkerProps.Static_executable
		if axis == bazel.NoConfigAxis {
			if linkBinaryShared := !proptools.Bool(staticExecutable); !linkBinaryShared {
				attrs.Linkshared = &linkBinaryShared
			}
		} else if staticExecutable != nil {
			// TODO(b/202876379): Static_executable is arch-variant; however, linkshared is a
			// nonconfigurable attribute. Only 4 AOSP modules use this feature, defer handling
			ctx.ModuleErrorf("bp2build cannot migrate a module with arch/target-specific static_executable values")
		}
		if suffix := linkerProps.Suffix; suffix != nil {
			attrs.Suffix.SetSelectValue(axis, config, suffix)
		}
	})

	return attrs
}
