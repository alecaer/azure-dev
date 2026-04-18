// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_UpdateAppServiceContainerImage_EmptyImage(t *testing.T) {
	cli := &AzureClient{}
	err := cli.UpdateAppServiceContainerImage(t.Context(), "sub", "rg", "app", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "image reference cannot be empty")
}

func Test_UpdateAppServiceContainerImage_WhitespaceImage(t *testing.T) {
	cli := &AzureClient{}
	err := cli.UpdateAppServiceContainerImage(t.Context(), "sub", "rg", "app", "", "   ")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "image reference cannot be empty")
}
