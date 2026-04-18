// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/azure/azure-dev/cli/azd/internal"
	"github.com/azure/azure-dev/cli/azd/internal/mapper"
	"github.com/azure/azure-dev/cli/azd/pkg/alpha"
	"github.com/azure/azure-dev/cli/azd/pkg/async"
	"github.com/azure/azure-dev/cli/azd/pkg/azapi"
	"github.com/azure/azure-dev/cli/azd/pkg/environment"
	"github.com/azure/azure-dev/cli/azd/pkg/input"
	"github.com/azure/azure-dev/cli/azd/pkg/tools"
)

type appServiceTarget struct {
	env             *environment.Environment
	envManager      environment.Manager
	cli             *azapi.AzureClient
	console         input.Console
	containerHelper *ContainerHelper
	alphaManager    *alpha.FeatureManager
}

// NewAppServiceTarget creates a new instance of the AppServiceTarget
func NewAppServiceTarget(
	env *environment.Environment,
	envManager environment.Manager,
	azCli *azapi.AzureClient,
	console input.Console,
	containerHelper *ContainerHelper,
	alphaManager *alpha.FeatureManager,
) ServiceTarget {
	return &appServiceTarget{
		env:             env,
		envManager:      envManager,
		cli:             azCli,
		console:         console,
		containerHelper: containerHelper,
		alphaManager:    alphaManager,
	}
}

// Gets the required external tools
func (st *appServiceTarget) RequiredExternalTools(ctx context.Context, serviceConfig *ServiceConfig) []tools.ExternalTool {
	if st.isContainerMode(serviceConfig) && st.containerHelper != nil {
		return st.containerHelper.RequiredExternalTools(ctx, serviceConfig)
	}
	return []tools.ExternalTool{}
}

// Initializes the AppService target
func (st *appServiceTarget) Initialize(ctx context.Context, serviceConfig *ServiceConfig) error {
	if !st.isContainerMode(serviceConfig) {
		return nil
	}
	return st.ensureContainerModeEnabled()
}

// isContainerMode reports whether the supplied service configuration
// indicates Web App for Containers deployment.
func (st *appServiceTarget) isContainerMode(serviceConfig *ServiceConfig) bool {
	return appServiceRequestsContainerMode(serviceConfig)
}

// ensureContainerModeEnabled returns an actionable error when a service
// requests container mode but the alpha feature flag is not enabled.
func (st *appServiceTarget) ensureContainerModeEnabled() error {
	if st.alphaManager == nil {
		return nil
	}
	if st.alphaManager.IsEnabled(FeatureAppServiceContainer) {
		return nil
	}
	return &internal.ErrorWithSuggestion{
		Err: fmt.Errorf(
			"Web App for Containers (host: appservice with a container image) is an alpha feature"),
		Suggestion: fmt.Sprintf(
			"Enable the feature with `%s`, then retry the command.",
			alpha.GetEnableCommand(FeatureAppServiceContainer)),
	}
}

// Prepares a zip archive from the specified build output, or in container
// mode skips packaging entirely because the image was produced by the docker during Build.
func (st *appServiceTarget) Package(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
	progress *async.Progress[ServiceProgress],
) (*ServicePackageResult, error) {
	if st.isContainerMode(serviceConfig) {
		return &ServicePackageResult{}, nil
	}

	progress.SetProgress(NewServiceProgress("Compressing deployment artifacts"))

	// Get package path from the service context
	var packagePath string
	if artifact, found := serviceContext.Package.FindFirst(WithKind(ArtifactKindDirectory)); found &&
		artifact.Location != "" {
		packagePath = artifact.Location
	}

	if packagePath == "" {
		return nil, fmt.Errorf("no package artifacts found in service context")
	}

	zipFilePath, err := createDeployableZip(
		serviceConfig,
		packagePath,
	)
	if err != nil {
		return nil, err
	}

	// Create zip artifact
	zipArtifact := &Artifact{
		Kind:         ArtifactKindArchive,
		Location:     zipFilePath,
		LocationKind: LocationKindLocal,
		Metadata: map[string]string{
			"packagePath": packagePath,
		},
	}

	return &ServicePackageResult{
		Artifacts: ArtifactCollection{zipArtifact},
	}, nil
}

// Publish is a no-op for zip-based deployments but drives the container
// image publish (tag + push to ACR) when the service is configured for
// Web App for Containers.
func (st *appServiceTarget) Publish(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
	targetResource *environment.TargetResource,
	progress *async.Progress[ServiceProgress],
	publishOptions *PublishOptions,
) (*ServicePublishResult, error) {
	if !st.isContainerMode(serviceConfig) {
		return &ServicePublishResult{}, nil
	}

	if st.containerHelper == nil {
		return nil, fmt.Errorf(
			"container helper is not configured for the app service target; " +
				"container mode cannot be used without it")
	}

	// If the package artifacts already reference a remote container image
	// (e.g. `azd deploy --from-package <image>`) there's nothing to push.
	if artifact, found := serviceContext.Package.FindFirst(WithKind(ArtifactKindContainer)); found &&
		artifact.LocationKind == LocationKindRemote && artifact.Location != "" {
		publishResult := &ServicePublishResult{
			Artifacts: ArtifactCollection{
				{
					Kind:         ArtifactKindContainer,
					Location:     artifact.Location,
					LocationKind: LocationKindRemote,
					Metadata: map[string]string{
						"remoteImage": artifact.Location,
					},
				},
			},
		}
		st.rememberImage(ctx, serviceConfig, artifact.Location)
		return publishResult, nil
	}

	publishResult, err := st.containerHelper.Publish(
		ctx, serviceConfig, serviceContext, targetResource, st.env, progress, publishOptions)
	if err != nil {
		return nil, err
	}

	if remote, ok := publishResult.Artifacts.FindFirst(WithKind(ArtifactKindContainer)); ok {
		st.rememberImage(ctx, serviceConfig, remote.Location)
	}

	return publishResult, nil
}

// rememberImage persists the published image under a well-known environment
// key (mirroring what the container app target does) so downstream tooling
// and subsequent runs can reference the last-deployed image.
func (st *appServiceTarget) rememberImage(ctx context.Context, serviceConfig *ServiceConfig, image string) {
	st.env.SetServiceProperty(serviceConfig.Name, "IMAGE_NAME", image)
	if st.envManager == nil {
		return
	}
	if err := st.envManager.Save(ctx, st.env); err != nil {
		// Don't fail the publish on persistence issues; the image was pushed
		// successfully and Deploy still receives it through the service context.
		// The env key is a convenience for users and downstream tooling.
		st.console.Message(ctx, fmt.Sprintf(
			"warning: failed to persist image name to environment: %v", err))
	}
}

// Deploy dispatches to the zip deploy path (default) or the container image
// update path (Web App for Containers) based on the service configuration.
func (st *appServiceTarget) Deploy(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
	targetResource *environment.TargetResource,
	progress *async.Progress[ServiceProgress],
) (*ServiceDeployResult, error) {
	if err := st.validateTargetResource(targetResource); err != nil {
		return nil, fmt.Errorf("validating target resource: %w", err)
	}

	if st.isContainerMode(serviceConfig) {
		return st.deployContainer(ctx, serviceConfig, serviceContext, targetResource, progress)
	}

	return st.deployZip(ctx, serviceConfig, serviceContext, targetResource, progress)
}

// deployZip is the original zip-based deploy path — unchanged in behavior.
func (st *appServiceTarget) deployZip(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
	targetResource *environment.TargetResource,
	progress *async.Progress[ServiceProgress],
) (*ServiceDeployResult, error) {
	// Get zip file path from package artifacts
	var zipFilePath string
	if artifact, found := serviceContext.Package.FindFirst(WithKind(ArtifactKindArchive)); found && artifact.Location != "" {
		zipFilePath = artifact.Location
	}

	if zipFilePath == "" {
		return nil, fmt.Errorf("no zip artifacts found in service context")
	}

	// Determine deployment targets based on deployment history and slots
	deployTargets, err := st.determineDeploymentTargets(ctx, serviceConfig, targetResource, progress)
	if err != nil {
		return nil, fmt.Errorf("determining deployment targets: %w", err)
	}

	// Check if runtime status tracking should be skipped for this service
	skipStatusCheckEnvVar := skipStatusCheckEnvVarNameForService(serviceConfig.Name)
	skipStatusCheck, _ := strconv.ParseBool(st.env.Getenv(skipStatusCheckEnvVar))

	// Deploy to each target
	hasSlots := len(deployTargets) > 1 || (len(deployTargets) == 1 && deployTargets[0].SlotName != "")

	for _, target := range deployTargets {
		zipFile, err := os.Open(zipFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed reading deployment zip file: %w", err)
		}
		defer zipFile.Close()

		var deployErr error
		if target.SlotName == "" {
			// When deploying to the main app
			var progressMsg string
			if hasSlots {
				// Mirror Azure Portal terminology: main app is the "production" slot
				progressMsg = "Uploading deployment package to production slot"
			} else {
				// No slots configured, use simple message
				progressMsg = "Uploading deployment package"
			}
			progress.SetProgress(NewServiceProgress(progressMsg))
			_, deployErr = st.cli.DeployAppServiceZip(
				ctx,
				targetResource.SubscriptionId(),
				targetResource.ResourceGroupName(),
				targetResource.ResourceName(),
				zipFile,
				func(logProgress string) { progress.SetProgress(NewServiceProgress(logProgress)) },
				skipStatusCheck,
			)
		} else {
			progressMsg := fmt.Sprintf("Uploading deployment package to slot '%s'", target.SlotName)
			progress.SetProgress(NewServiceProgress(progressMsg))
			_, deployErr = st.cli.DeployAppServiceSlotZip(
				ctx,
				targetResource.SubscriptionId(),
				targetResource.ResourceGroupName(),
				targetResource.ResourceName(),
				target.SlotName,
				zipFile,
				func(logProgress string) { progress.SetProgress(NewServiceProgress(logProgress)) },
			)
		}

		if deployErr != nil {
			var targetName string
			if target.SlotName == "" {
				if hasSlots {
					targetName = "production slot"
				} else {
					targetName = "app service"
				}
			} else {
				targetName = fmt.Sprintf("slot '%s'", target.SlotName)
			}
			return nil, fmt.Errorf("deploying service %s to %s: %w", serviceConfig.Name, targetName, deployErr)
		}
	}

	progress.SetProgress(NewServiceProgress("Fetching endpoints for app service"))
	endpoints, err := st.Endpoints(ctx, serviceConfig, targetResource)
	if err != nil {
		return nil, err
	}

	artifacts := ArtifactCollection{}

	// Add endpoints as artifacts
	for _, endpoint := range endpoints {
		if err := artifacts.Add(&Artifact{
			Kind:         ArtifactKindEndpoint,
			Location:     endpoint,
			LocationKind: LocationKindRemote,
		}); err != nil {
			return nil, fmt.Errorf("failed to add endpoint artifact: %w", err)
		}
	}

	// Add resource artifact
	var resourceArtifact *Artifact
	if err := mapper.Convert(targetResource, &resourceArtifact); err == nil {
		if err := artifacts.Add(resourceArtifact); err != nil {
			return nil, fmt.Errorf("failed to add resource artifact: %w", err)
		}
	}

	return &ServiceDeployResult{
		Artifacts: artifacts,
	}, nil
}

// deployContainer implements the Web App for Containers deploy path.
// Updating the image reference itself is the deploy trigger.
func (st *appServiceTarget) deployContainer(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
	targetResource *environment.TargetResource,
	progress *async.Progress[ServiceProgress],
) (*ServiceDeployResult, error) {
	progress.SetProgress(NewServiceProgress("Verifying app service is Linux"))
	isLinux, err := st.cli.IsLinuxAppService(
		ctx,
		targetResource.SubscriptionId(),
		targetResource.ResourceGroupName(),
		targetResource.ResourceName(),
	)
	if err != nil {
		return nil, fmt.Errorf("checking app service kind: %w", err)
	}
	if !isLinux {
		return nil, &internal.ErrorWithSuggestion{
			Err: fmt.Errorf(
				"app service '%s' is not a Linux site; Web App for Containers deployment requires Linux",
				targetResource.ResourceName()),
			Suggestion: "Recreate the Web App with `kind: 'app,linux'` and a Linux App Service Plan, " +
				"or use `host: containerapp` for a platform-managed container target.",
		}
	}

	imageName := st.resolveContainerImage(serviceConfig, serviceContext)
	if imageName == "" {
		return nil, fmt.Errorf(
			"no container image available for service '%s'; ensure the publish step pushed "+
				"an image to the registry (or that SERVICE_%s_IMAGE_NAME is set)",
			serviceConfig.Name,
			strings.ToUpper(strings.ReplaceAll(serviceConfig.Name, "-", "_")),
		)
	}

	deployTargets, err := st.determineDeploymentTargets(ctx, serviceConfig, targetResource, progress)
	if err != nil {
		return nil, fmt.Errorf("determining deployment targets: %w", err)
	}
	hasSlots := len(deployTargets) > 1 || (len(deployTargets) == 1 && deployTargets[0].SlotName != "")

	for _, dt := range deployTargets {
		var progressMsg string
		if dt.SlotName == "" {
			if hasSlots {
				progressMsg = "Updating container image on production slot"
			} else {
				progressMsg = "Updating container image"
			}
		} else {
			progressMsg = fmt.Sprintf("Updating container image on slot '%s'", dt.SlotName)
		}
		progress.SetProgress(NewServiceProgress(progressMsg))

		if err := st.cli.UpdateAppServiceContainerImage(
			ctx,
			targetResource.SubscriptionId(),
			targetResource.ResourceGroupName(),
			targetResource.ResourceName(),
			dt.SlotName,
			imageName,
		); err != nil {
			var targetName string
			switch {
			case dt.SlotName == "" && hasSlots:
				targetName = "production slot"
			case dt.SlotName == "":
				targetName = "app service"
			default:
				targetName = fmt.Sprintf("slot '%s'", dt.SlotName)
			}
			return nil, fmt.Errorf(
				"updating container image for service %s on %s: %w",
				serviceConfig.Name, targetName, err)
		}
	}

	progress.SetProgress(NewServiceProgress("Fetching endpoints for app service"))
	endpoints, err := st.Endpoints(ctx, serviceConfig, targetResource)
	if err != nil {
		return nil, err
	}

	artifacts := ArtifactCollection{
		&Artifact{
			Kind:         ArtifactKindContainer,
			Location:     imageName,
			LocationKind: LocationKindRemote,
			Metadata: map[string]string{
				"remoteImage": imageName,
			},
		},
	}
	for _, endpoint := range endpoints {
		if err := artifacts.Add(&Artifact{
			Kind:         ArtifactKindEndpoint,
			Location:     endpoint,
			LocationKind: LocationKindRemote,
		}); err != nil {
			return nil, fmt.Errorf("failed to add endpoint artifact: %w", err)
		}
	}

	var resourceArtifact *Artifact
	if err := mapper.Convert(targetResource, &resourceArtifact); err == nil {
		if err := artifacts.Add(resourceArtifact); err != nil {
			return nil, fmt.Errorf("failed to add resource artifact: %w", err)
		}
	}

	return &ServiceDeployResult{Artifacts: artifacts}, nil
}

// resolveContainerImage returns the remote image reference to deploy.
func (st *appServiceTarget) resolveContainerImage(
	serviceConfig *ServiceConfig,
	serviceContext *ServiceContext,
) string {
	if serviceContext != nil {
		if artifact, found := serviceContext.Publish.FindFirst(WithKind(ArtifactKindContainer)); found &&
			artifact.Location != "" {
			return artifact.Location
		}
		if artifact, found := serviceContext.Package.FindFirst(WithKind(ArtifactKindContainer)); found &&
			artifact.LocationKind == LocationKindRemote && artifact.Location != "" {
			return artifact.Location
		}
	}
	if st.env != nil {
		if image := st.env.GetServiceProperty(serviceConfig.Name, "IMAGE_NAME"); image != "" {
			return image
		}
	}
	return ""
}

// productionSlotName is the reserved platform name for the main app.
// Azure does not allow creating a deployment slot named "production" —
// the ARM API rejects it with: "Slot name: 'Production' is reserved."
// This was verified via: az webapp deployment slot create --slot production
// Azure CLI, PowerShell, and the Azure Portal all use "production" to refer to the main app.
const productionSlotName = "production"

// deploymentTarget represents a target for deployment (main app or a slot)
type deploymentTarget struct {
	SlotName string // Empty string means main app
}

// determineDeploymentTargets determines which targets (main app and/or slots) to deploy to.
//
// Deployment target selection:
//  1. SLOT_NAME takes highest precedence — explicit intent always wins.
//     "production" means the main app. Any other value must match an existing slot.
//  2. No slots exist — deploy to main app.
//  3. Slots exist + interactive — prompt user to select (includes "production" for main app).
//  4. Slots exist + --no-prompt — fail with error listing available targets.
func (st *appServiceTarget) determineDeploymentTargets(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	targetResource *environment.TargetResource,
	progress *async.Progress[ServiceProgress],
) ([]deploymentTarget, error) {
	slotEnvVarName := slotEnvVarNameForService(serviceConfig.Name)

	// Check SLOT_NAME first — explicit intent always wins
	if slotName := st.env.Getenv(slotEnvVarName); slotName != "" {
		// "production" is the platform-reserved name for the main app
		if strings.EqualFold(slotName, productionSlotName) {
			progress.SetProgress(NewServiceProgress("Deploying to production (main app)"))
			return []deploymentTarget{{SlotName: ""}}, nil
		}

		// Validate that the specified slot exists
		progress.SetProgress(NewServiceProgress("Checking deployment slots"))
		slots, err := st.cli.GetAppServiceSlots(
			ctx,
			targetResource.SubscriptionId(),
			targetResource.ResourceGroupName(),
			targetResource.ResourceName(),
		)
		if err != nil {
			return nil, fmt.Errorf("getting deployment slots: %w", err)
		}

		for _, slot := range slots {
			if strings.EqualFold(slot.Name, slotName) {
				return []deploymentTarget{{SlotName: slot.Name}}, nil
			}
		}

		availableSlots := make([]string, len(slots))
		for i, slot := range slots {
			availableSlots[i] = slot.Name
		}
		return nil, fmt.Errorf(
			"slot '%s' specified in %s not found. Available slots: [%s]. "+
				"Use '%s=%s' to deploy to the main app",
			slotName, slotEnvVarName, strings.Join(availableSlots, ", "),
			slotEnvVarName, productionSlotName)
	}

	// No SLOT_NAME set — check if slots exist
	progress.SetProgress(NewServiceProgress("Checking deployment slots"))
	slots, err := st.cli.GetAppServiceSlots(
		ctx,
		targetResource.SubscriptionId(),
		targetResource.ResourceGroupName(),
		targetResource.ResourceName(),
	)
	if err != nil {
		return nil, fmt.Errorf("getting deployment slots: %w", err)
	}

	// No slots — deploy to main app
	if len(slots) == 0 {
		return []deploymentTarget{{SlotName: ""}}, nil
	}

	// Slots exist + --no-prompt — fail with clear error
	if st.console.IsNoPromptMode() {
		availableTargets := []string{productionSlotName}
		for _, slot := range slots {
			availableTargets = append(availableTargets, slot.Name)
		}
		return nil, fmt.Errorf(
			"deployment slots detected but no target specified. "+
				"Set %s to one of: [%s] ('production' = main app)",
			slotEnvVarName, strings.Join(availableTargets, ", "))
	}

	// Slots exist + interactive — prompt user including main app option
	slotOptions := []string{fmt.Sprintf("%s (main app)", productionSlotName)}
	for _, slot := range slots {
		slotOptions = append(slotOptions, slot.Name)
	}

	selectedIndex, err := st.console.Select(ctx, input.ConsoleOptions{
		Message: fmt.Sprintf(
			"Select a deployment target\nNote: skip this prompt with '%s=<target>'\n",
			slotEnvVarName),
		Options: slotOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("selecting deployment target: %w", err)
	}

	// Index 0 = production (main app)
	if selectedIndex == 0 {
		return []deploymentTarget{{SlotName: ""}}, nil
	}

	return []deploymentTarget{{SlotName: slots[selectedIndex-1].Name}}, nil
}

// slotEnvVarNameForService returns the environment variable name for setting the deployment slot
// for a given service. The format is AZD_DEPLOY_{SERVICE_NAME}_SLOT_NAME where the service name
// is normalized via environment.Key (uppercase, spaces/hyphens → underscores).
func slotEnvVarNameForService(serviceName string) string {
	return fmt.Sprintf("AZD_DEPLOY_%s_SLOT_NAME", environment.Key(serviceName))
}

// skipStatusCheckEnvVarNameForService returns the environment variable name for skipping
// deployment status tracking for a given service. The format is
// AZD_DEPLOY_{SERVICE_NAME}_SKIP_STATUS_CHECK where the service name
// is uppercase and any hyphens are replaced with underscores.
func skipStatusCheckEnvVarNameForService(serviceName string) string {
	normalizedName := strings.ToUpper(strings.ReplaceAll(serviceName, "-", "_"))
	return fmt.Sprintf("AZD_DEPLOY_%s_SKIP_STATUS_CHECK", normalizedName)
}

// Gets the exposed endpoints for the App Service, including any deployment slots
func (st *appServiceTarget) Endpoints(
	ctx context.Context,
	serviceConfig *ServiceConfig,
	targetResource *environment.TargetResource,
) ([]string, error) {
	// Get main app properties
	appServiceProperties, err := st.cli.GetAppServiceProperties(
		ctx,
		targetResource.SubscriptionId(),
		targetResource.ResourceGroupName(),
		targetResource.ResourceName(),
	)
	if err != nil {
		return nil, fmt.Errorf("fetching service properties: %w", err)
	}

	var endpoints []string

	// Add main app endpoints
	for _, hostName := range appServiceProperties.HostNames {
		endpoints = append(endpoints, fmt.Sprintf("https://%s/", hostName))
	}

	// Get all deployment slots
	slots, err := st.cli.GetAppServiceSlots(
		ctx,
		targetResource.SubscriptionId(),
		targetResource.ResourceGroupName(),
		targetResource.ResourceName(),
	)
	if err != nil {
		// Log but don't fail if we can't get slots - main app endpoints are still valid
		return endpoints, nil
	}

	// Add slot endpoints with prefix
	for _, slot := range slots {
		slotProperties, err := st.cli.GetAppServiceSlotProperties(
			ctx,
			targetResource.SubscriptionId(),
			targetResource.ResourceGroupName(),
			targetResource.ResourceName(),
			slot.Name,
		)
		if err != nil {
			// Skip this slot if we can't get its properties
			continue
		}

		for _, hostName := range slotProperties.HostNames {
			endpoints = append(endpoints, fmt.Sprintf("https://%s/", hostName))
		}
	}

	return endpoints, nil
}

func (st *appServiceTarget) validateTargetResource(
	targetResource *environment.TargetResource,
) error {
	if !strings.EqualFold(targetResource.ResourceType(), string(azapi.AzureResourceTypeWebSite)) {
		return resourceTypeMismatchError(
			targetResource.ResourceName(),
			targetResource.ResourceType(),
			azapi.AzureResourceTypeWebSite,
		)
	}

	return nil
}
