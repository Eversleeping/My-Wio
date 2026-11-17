package runtimeaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const debianBackports = "debian-backports"

// A private catalog adds one fixed, signed distribution source for this
// operation only. Neither persistent sources nor system APT lists are changed.
func (e EnvironmentInstaller) backportsCatalog(ctx context.Context) (EnvironmentInstaller, func(), error) {
	keyring := filepath.Join(e.Root, "/usr/share/keyrings/debian-archive-keyring.gpg")
	if _, err := os.Stat(keyring); err != nil {
		return e, func() {}, errors.New("The installed Debian archive keyring is required to verify the official backports repository.")
	}
	dir, err := os.MkdirTemp("", "wio-isolation-apt-")
	if err != nil {
		return e, func() {}, errors.New("Could not create a private temporary package catalog.")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fail := func() (EnvironmentInstaller, func(), error) {
		cleanup()
		return e, func() {}, errors.New("Could not prepare the private package catalog.")
	}
	for _, name := range []string{"lists/partial", "cache/archives/partial"} {
		if os.MkdirAll(filepath.Join(dir, name), 0700) != nil {
			return fail()
		}
	}
	base, err := os.ReadFile(filepath.Join(e.Root, "/etc/apt/sources.list"))
	if err != nil && !os.IsNotExist(err) {
		return fail()
	}
	source := append(base, []byte("\ndeb [signed-by="+keyring+"] https://deb.debian.org/debian trixie-backports main\n")...)
	if os.WriteFile(filepath.Join(dir, "sources.list"), source, 0600) != nil {
		return fail()
	}
	// Inspection may run in a read-only host mount. Disable update hooks, which
	// can otherwise mutate the host even when all APT state is redirected.
	config := "#clear APT::Update::Post-Invoke;\n#clear APT::Update::Post-Invoke-Success;\n#clear APT::Update::Pre-Invoke;\n"
	if os.WriteFile(filepath.Join(dir, "apt.conf"), []byte(config), 0600) != nil {
		return fail()
	}
	e.aptOptions = []string{"-c", filepath.Join(dir, "apt.conf"),
		"-o", "Dir::Etc::sourcelist=" + filepath.Join(dir, "sources.list"),
		"-o", "Dir::Etc::sourceparts=" + filepath.Join(e.Root, "/etc/apt/sources.list.d"),
		"-o", "Dir::State::lists=" + filepath.Join(dir, "lists"),
		"-o", "Dir::Cache=" + filepath.Join(dir, "cache"),
		"-o", "APT::Sandbox::User=root", "-o", "Acquire::Languages=none",
		"-o", "Acquire::Retries=0", "-o", "Acquire::http::Timeout=20", "-o", "Acquire::https::Timeout=20",
		"-o", "APT::Update::Error-Mode=any",
		"-o", "Acquire::AllowInsecureRepositories=false", "-o", "Acquire::AllowDowngradeToInsecureRepositories=false",
		"-o", "Acquire::Check-Valid-Until=true", "-o", "APT::Get::AllowUnauthenticated=false"}
	if output, err := e.apt(ctx, "apt-get", "update"); err != nil {
		cleanup()
		return e, func() {}, errors.New("Official backports catalog could not be verified: " + packageFailure(output))
	}
	return e, cleanup, nil
}

func (e EnvironmentInstaller) apt(ctx context.Context, program string, args ...string) (string, error) {
	if len(e.aptOptions) > 0 && program == "apt-get" {
		for _, arg := range args {
			if arg == "install" {
				// Kernel dependencies such as linux-base can also come from
				// backports. Apply the target release to this transaction only.
				args = append([]string{"-t", "trixie-backports"}, args...)
				break
			}
		}
	}
	return e.Run(ctx, program, append(append([]string{}, e.aptOptions...), args...)...)
}
