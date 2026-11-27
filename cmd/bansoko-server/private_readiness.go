package main

import (
	bootstrapcmd "bansoko/internal/runtime/bootstrap"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
)

type privateDeploymentIdentity struct{ deploymentIdentityProvider }

func (p privateDeploymentIdentity) CurrentPointer() deploymentidentity.PointerState {
	pointer := p.deploymentIdentityProvider.CurrentPointer()
	if p.Identity().SelectedSource == deploymentidentity.SourceBase && pointer.Presented &&
		bootstrapcmd.VerifyPrivateBasePointer(p.updatesRoot, buildinfo.Current(), pointer) == nil {
		return deploymentidentity.PointerState{}
	}
	return pointer
}

func (p privateDeploymentIdentity) Validate() error {
	if p.Identity().SelectedSource != deploymentidentity.SourceBase {
		return p.deploymentIdentityProvider.Validate()
	}
	identity := p.Identity()
	identity.ProcessBuildID = buildinfo.Current().BuildID
	pointer := p.CurrentPointer()
	return identity.ValidateForRuntime(&pointer, true)
}
