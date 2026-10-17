package sshbootstrap

// Keep dependency setup separate from enrollment: repair uses the same path,
// and every installed tool is checked again as the service account.
func ensureBootstrapDependencies(request InstallRequest, probe inspection, run func(string) (string, error), registry string) (inspection, []string) {
	warnings := []string{}
	if !probe.NPMReady {
		request.report("installing_node", 0, 0)
		_, _ = run(nodeInstallationScript(registry))
	}
	_, nodeErr := run(agentNodeVerificationScript())
	probe.NPMReady = nodeErr == nil
	if !probe.NPMReady {
		warnings = append(warnings, "node_install_failed")
	}
	if !probe.DockerReady || !probe.ComposeReady || !probe.GitReady {
		request.report("installing_deployment_dependencies", 0, 0)
		_, _ = run("/usr/local/bin/wio-agent prerequisite-helper --once")
	}
	_, dockerErr := run("su -s /bin/sh -c 'docker info --format \"{{.ServerVersion}}\"' wio-agent")
	_, composeErr := run("su -s /bin/sh -c 'docker compose version --short' wio-agent")
	_, gitErr := run("su -s /bin/sh -c 'git --version' wio-agent")
	probe.DockerReady = dockerErr == nil
	probe.ComposeReady = composeErr == nil
	probe.GitReady = gitErr == nil
	if !probe.DockerReady || !probe.ComposeReady || !probe.GitReady {
		warnings = append(warnings, "deployment_prerequisites_install_failed")
	}
	return probe, warnings
}

func agentNodeVerificationScript() string {
	return "su -s /bin/sh -c " + shellQuote(`node -e 'if (Number(process.versions.node.split(".")[0]) < 20) process.exit(1)' && npm --version`) + " wio-agent"
}

func nodeInstallationScript(registry string) string {
	source := "https://nodejs.org/dist/latest-v22.x"
	if registry == mainlandNPMRegistry {
		source = "https://registry.npmmirror.com/-/binary/node/latest-v22.x"
	}
	return `set -eu
if node -e 'if (Number(process.versions.node.split(".")[0]) < 20) process.exit(1)' >/dev/null 2>&1 && npm --version >/dev/null 2>&1; then exit 0; fi
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  apt-get install -y ca-certificates curl tar xz-utils
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y ca-certificates curl tar xz
elif command -v yum >/dev/null 2>&1; then
  yum install -y ca-certificates curl tar xz
else
  echo 'Automatic Node.js setup requires apt-get, dnf, or yum' >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64|amd64) node_arch=x64 ;;
  aarch64|arm64) node_arch=arm64 ;;
  *) echo 'Unsupported Node.js architecture' >&2; exit 1 ;;
esac
node_tmp=$(mktemp -d)
trap 'rm -rf "$node_tmp"' EXIT
node_source=` + shellQuote(source) + `
if ! curl -fSL --retry 2 --connect-timeout 15 --max-time 60 "$node_source/SHASUMS256.txt" -o "$node_tmp/SHASUMS256.txt"; then
  node_source=https://nodejs.org/dist/latest-v22.x
  curl -fSL --retry 2 --connect-timeout 15 --max-time 60 "$node_source/SHASUMS256.txt" -o "$node_tmp/SHASUMS256.txt"
fi
node_file=$(awk -v arch="$node_arch" '$2 ~ "^node-v22\\.[0-9]+\\.[0-9]+-linux-" arch "\\.tar\\.xz$" { print $2; exit }' "$node_tmp/SHASUMS256.txt")
test -n "$node_file"
curl -fSL --retry 2 --connect-timeout 15 --max-time 600 "$node_source/$node_file" -o "$node_tmp/$node_file"
(cd "$node_tmp" && awk -v file="$node_file" '$2 == file { print }' SHASUMS256.txt | sha256sum -c -)
install -d -m 0755 /usr/local/lib/wio-node
tar -xJf "$node_tmp/$node_file" -C /usr/local/lib/wio-node --no-same-owner
node_runtime=/usr/local/lib/wio-node/${node_file%.tar.xz}
for tool in node npm npx; do
  test -x "$node_runtime/bin/$tool"
  ln -sfn "$node_runtime/bin/$tool" "/usr/local/bin/$tool"
done
node --version
npm --version`
}
