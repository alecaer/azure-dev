// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v2"
)

// UpdateAppServiceContainerImage updates the container image reference for a
// Linux App Service ("Web App for Containers"). It sets siteConfig.linuxFxVersion
// to "DOCKER|<image>" via the dedicated Configuration update API. When slotName
// is empty the change is applied to the main site; otherwise the named slot is
// updated.
//
// The image update itself is the deploy trigger — callers should NOT follow
// this with an explicit Restart. Updating to a new tag or digest will cause
// the platform to pull the new image.
//
// This helper assumes the site has already been verified to be Linux. It does
// not configure registry credentials; registry auth is expected to be set by
// the infrastructure layer (Bicep/Terraform), typically using a system-assigned
// managed identity with AcrPull on the target Azure Container Registry, or
// alternatively via DOCKER_REGISTRY_SERVER_* app settings for admin auth.
func (cli *AzureClient) UpdateAppServiceContainerImage(
	ctx context.Context,
	subscriptionId string,
	resourceGroup string,
	appName string,
	slotName string,
	image string,
) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("image reference cannot be empty")
	}

	client, err := cli.createWebAppsClient(ctx, subscriptionId)
	if err != nil {
		return err
	}

	linuxFxVersion := fmt.Sprintf("DOCKER|%s", image)
	siteConfig := armappservice.SiteConfigResource{
		Properties: &armappservice.SiteConfig{
			LinuxFxVersion: &linuxFxVersion,
		},
	}

	if slotName == "" {
		if _, err := client.UpdateConfiguration(ctx, resourceGroup, appName, siteConfig, nil); err != nil {
			return fmt.Errorf("updating app service '%s' container image: %w", appName, err)
		}
		return nil
	}

	if _, err := client.UpdateConfigurationSlot(
		ctx, resourceGroup, appName, slotName, siteConfig, nil,
	); err != nil {
		return fmt.Errorf(
			"updating app service '%s' slot '%s' container image: %w", appName, slotName, err)
	}
	return nil
}

// IsLinuxAppService reports whether the specified App Service resource runs
// Linux. It is used by the appservice service target to reject container-mode
// deployments on Windows sites early, with a clear error message.
func (cli *AzureClient) IsLinuxAppService(
	ctx context.Context,
	subscriptionId string,
	resourceGroup string,
	appName string,
) (bool, error) {
	app, err := cli.appService(ctx, subscriptionId, resourceGroup, appName)
	if err != nil {
		return false, err
	}
	// "app,linux" indicates a Linux Web App (code or container). Windows sites
	// report "app" without the ",linux" suffix.
	if app.Kind == nil {
		return false, nil
	}
	return strings.Contains(strings.ToLower(*app.Kind), "linux"), nil
}
