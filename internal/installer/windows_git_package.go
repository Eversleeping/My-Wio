package installer

// Ship a verified PortableGit with the control plane so Windows hosts which
// reach Wio can install Git without also needing access to GitHub's release API.
const WindowsGitArchive = "wio-git-windows-amd64.7z.exe"
const WindowsGitSHA256 = "9dd04a608ceb5f6ab32a73709e0ecb159d211da71b697c45ff81f6dd0c6b49c7"
const WindowsGitDownloadURL = "https://github.com/git-for-windows/git/releases/download/v2.56.1.windows.1/PortableGit-2.56.1-64-bit.7z.exe"

func windowsGitPackageURL(controlURL string) string {
	return controlURL + "/api/agent/install-tools/windows-git?sha256=" + WindowsGitSHA256
}
