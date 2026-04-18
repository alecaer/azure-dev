// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/osutil"
	"github.com/stretchr/testify/assert"
)

func Test_appServiceRequestsContainerMode(t *testing.T) {
	cases := []struct {
		name     string
		config   *ServiceConfig
		expected bool
	}{
		{
			name:     "nil",
			config:   nil,
			expected: false,
		},
		{
			name:     "appservice zip (no signals)",
			config:   &ServiceConfig{Host: AppServiceTarget, Language: ServiceLanguagePython},
			expected: false,
		},
		{
			name:     "appservice language docker",
			config:   &ServiceConfig{Host: AppServiceTarget, Language: ServiceLanguageDocker},
			expected: true,
		},
		{
			name: "appservice top-level image",
			config: &ServiceConfig{
				Host:  AppServiceTarget,
				Image: osutil.NewExpandableString("myacr.azurecr.io/app:v1"),
			},
			expected: true,
		},
		{
			name: "appservice docker.image",
			config: &ServiceConfig{
				Host: AppServiceTarget,
				Docker: DockerProjectOptions{
					Image: osutil.NewExpandableString("myacr.azurecr.io/app:v1"),
				},
			},
			expected: true,
		},
		{
			name: "unspecified host with image defaults to appservice container",
			config: &ServiceConfig{
				Host:  NonSpecifiedTarget,
				Image: osutil.NewExpandableString("myacr.azurecr.io/app:v1"),
			},
			expected: true,
		},
		{
			name:     "containerapp host is not treated as appservice container",
			config:   &ServiceConfig{Host: ContainerAppTarget, Language: ServiceLanguageDocker},
			expected: false,
		},
		{
			name:     "function host with docker language does not qualify",
			config:   &ServiceConfig{Host: AzureFunctionTarget, Language: ServiceLanguageDocker},
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, appServiceRequestsContainerMode(tc.config))
		})
	}
}

func Test_ServiceConfig_RequiresContainer(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var sc *ServiceConfig
		assert.False(t, sc.RequiresContainer())
	})

	t.Run("containerapp host always requires container", func(t *testing.T) {
		sc := &ServiceConfig{Host: ContainerAppTarget, Language: ServiceLanguagePython}
		assert.True(t, sc.RequiresContainer())
	})

	t.Run("appservice zip does not require container", func(t *testing.T) {
		sc := &ServiceConfig{Host: AppServiceTarget, Language: ServiceLanguagePython}
		assert.False(t, sc.RequiresContainer())
	})

	t.Run("appservice with docker language requires container", func(t *testing.T) {
		sc := &ServiceConfig{Host: AppServiceTarget, Language: ServiceLanguageDocker}
		assert.True(t, sc.RequiresContainer())
	})

	t.Run("appservice with docker.image requires container", func(t *testing.T) {
		sc := &ServiceConfig{
			Host: AppServiceTarget,
			Docker: DockerProjectOptions{
				Image: osutil.NewExpandableString("myacr.azurecr.io/app:v1"),
			},
		}
		assert.True(t, sc.RequiresContainer())
	})
}
