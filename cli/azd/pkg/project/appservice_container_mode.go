// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"github.com/azure/azure-dev/cli/azd/pkg/alpha"
)

// FeatureAppServiceContainer gates the alpha "Web App for Containers"
// deployment path for host: appservice. When the flag is off, any appservice
// service whose configuration signals container mode fails with a clear
// enable-the-flag suggestion.
var FeatureAppServiceContainer alpha.FeatureId = alpha.MustFeatureKey("appservice.container")

// appServiceRequestsContainerMode returns true when the ServiceConfig is
// using host: appservice with signals that indicate container (image)
// deployment rather than classic zip-deploy.
func appServiceRequestsContainerMode(serviceConfig *ServiceConfig) bool {
	if serviceConfig == nil {
		return false
	}
	if serviceConfig.Host != AppServiceTarget && serviceConfig.Host != NonSpecifiedTarget {
		return false
	}
	if serviceConfig.Language == ServiceLanguageDocker {
		return true
	}
	if !serviceConfig.Image.Empty() {
		return true
	}
	if !serviceConfig.Docker.Image.Empty() {
		return true
	}
	return false
}

// RequiresContainer returns true when this specific ServiceConfig effectively
// runs in container mode. This is a superset of ServiceTargetKind.RequiresContainer()
// that also covers host: appservice configured with a container image.
func (sc *ServiceConfig) RequiresContainer() bool {
	if sc == nil {
		return false
	}
	if sc.Host.RequiresContainer() {
		return true
	}
	return appServiceRequestsContainerMode(sc)
}
