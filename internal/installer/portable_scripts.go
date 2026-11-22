package installer

const macAccountScript = `set -eu
account=_wio-agent
if dscl . -read /Users/$account >/dev/null 2>&1; then
 [ "$(dscl . -read /Users/$account NFSHomeDirectory | cut -d' ' -f2-)" = '/Library/Application Support/Wio' ] || exit 1
else
 uid=400
 while dscl . -search /Users UniqueID "$uid" | grep -q . || dscl . -search /Groups PrimaryGroupID "$uid" | grep -q .; do uid=$((uid+1)); [ "$uid" -lt 500 ] || exit 1; done
 dscl . -create /Groups/$account
 dscl . -create /Groups/$account PrimaryGroupID "$uid"
 dscl . -create /Users/$account
 dscl . -create /Users/$account UniqueID "$uid"
 dscl . -create /Users/$account PrimaryGroupID "$uid"
 dscl . -create /Users/$account NFSHomeDirectory '/Library/Application Support/Wio'
 dscl . -create /Users/$account UserShell /usr/bin/false
 dscl . -create /Users/$account IsHidden 1
 dscl . -create /Users/$account Password '*'
fi
`

const macServicePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.wio.agent</string>
<key>ProgramArguments</key><array><string>__BINARY__</string><string>run</string><string>--config</string><string>__CONFIG__</string></array>
<key>UserName</key><string>_wio-agent</string><key>GroupName</key><string>_wio-agent</string>
<key>WorkingDirectory</key><string>__STATE__</string>
<key>EnvironmentVariables</key><dict>
<key>HOME</key><string>__STATE__</string>
<key>CODEX_HOME</key><string>__STATE__/.Codex</string>
<key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
</dict>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>5</integer><key>Umask</key><integer>63</integer>
<key>ExitTimeOut</key><integer>25</integer>
<key>StandardOutPath</key><string>__STATE__/agent.log</string>
<key>StandardErrorPath</key><string>__STATE__/agent-error.log</string>
</dict></plist>
`

// Fixed Node LTS package; archive integrity is checked before extraction.
const macNodeScript = `set -eu
version=v22.22.0
case "$(uname -m)" in x86_64) arch=x64;; arm64) arch=arm64;; *) exit 1;; esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
name=node-$version-darwin-$arch
curl --proto '=https' -fsSL --retry 3 "https://nodejs.org/dist/$version/SHASUMS256.txt" -o "$work/hashes"
curl --proto '=https' -fsSL --retry 3 "https://nodejs.org/dist/$version/$name.tar.gz" -o "$work/$name.tar.gz"
(cd "$work"; grep "  $name.tar.gz$" hashes | shasum -a 256 -c -)
tar -xzf "$work/$name.tar.gz" -C "$work"
mkdir -p /usr/local/bin /usr/local/lib
cp -R "$work/$name/lib/node_modules" /usr/local/lib/
install -m 0755 "$work/$name/bin/node" /usr/local/bin/node
ln -sf /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm
ln -sf /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
`
const windowsNodeScript = `$ErrorActionPreference='Stop'
[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12
$version='v22.22.0'
$name="node-$version-win-x64"
$work=Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $work | Out-Null
try {
 $hashes=(Invoke-WebRequest -UseBasicParsing "https://nodejs.org/dist/$version/SHASUMS256.txt").Content
 $match=[regex]::Match($hashes,'(?m)^([a-f0-9]{64})  '+[regex]::Escape($name+'.zip')+'\r?$')
 if(!$match.Success){throw 'Node checksum missing'}
 $zip=Join-Path $work 'node.zip'
 Invoke-WebRequest -UseBasicParsing "https://nodejs.org/dist/$version/$name.zip" -OutFile $zip
 if((Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $match.Groups[1].Value){throw 'Node checksum mismatch'}
 Expand-Archive $zip -DestinationPath $work
 $target=__WIO_NODE_DIR__
 if(Test-Path $target){Remove-Item $target -Force -Recurse}
 Move-Item (Join-Path $work $name) $target
} finally {Remove-Item $work -Force -Recurse}
`

const windowsGitScript = `$ErrorActionPreference='Stop'
[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12
$expected=__WIO_GIT_SHA256__
$work=Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $work | Out-Null
try {
 $exe=Join-Path $work 'git.exe'
 Invoke-WebRequest -UseBasicParsing __WIO_GIT_SOURCE__ -OutFile $exe -ErrorAction Stop
 if((Get-FileHash $exe -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected){throw 'Git checksum mismatch'}
 $target=__WIO_GIT_DIR__
 $stage=Join-Path $work 'extracted'
 & $exe -y ('-o'+$stage) | Out-Null
 if($LASTEXITCODE -ne 0){throw 'Git extraction failed'}
 & (Join-Path $stage 'cmd\git.exe') --version | Out-Null
 if($LASTEXITCODE -ne 0){throw 'Git version verification failed'}
 if(Test-Path -LiteralPath $target){Remove-Item -LiteralPath $target -Force -Recurse}
 Move-Item -LiteralPath $stage -Destination $target
} finally {Remove-Item -Force -Recurse $work}
`
