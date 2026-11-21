// Package sshbootstrap preserves the SSH bootstrap API. Installation steps live
// in installer and are shared with the target-machine command.
package sshbootstrap

import "github.com/wio-platform/wio/internal/installer"

type Target = installer.Target
type HostTarget = installer.HostTarget
type HostKeyResult = installer.HostKeyResult
type InstallRequest = installer.InstallRequest
type InstallProgress = installer.InstallProgress
type InstallResult = installer.InstallResult
type UninstallRequest = installer.UninstallRequest
type UninstallResult = installer.UninstallResult
type Service = installer.Service
type AccessSettings = installer.AccessSettings
type AccessRequest = installer.AccessRequest

var New = installer.New

var (
	ErrInvalidTarget          = installer.ErrInvalidTarget
	ErrHostKeyMismatch        = installer.ErrHostKeyMismatch
	ErrConnection             = installer.ErrConnection
	ErrAuthentication         = installer.ErrAuthentication
	ErrPrivilegeRequired      = installer.ErrPrivilegeRequired
	ErrUnsupportedPlatform    = installer.ErrUnsupportedPlatform
	ErrAssetsUnavailable      = installer.ErrAssetsUnavailable
	ErrCLIReleasesUnavailable = installer.ErrCLIReleasesUnavailable
	ErrInstallation           = installer.ErrInstallation
	ErrHeartbeatPending       = installer.ErrHeartbeatPending
	ErrServerMismatch         = installer.ErrServerMismatch
	ErrUninstallation         = installer.ErrUninstallation
	ErrAccessSettings         = installer.ErrAccessSettings
	ErrAccessChanged          = installer.ErrAccessChanged
)
