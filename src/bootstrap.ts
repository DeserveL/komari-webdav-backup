import { RUNNER_API } from './types';

export type Binaries = Record<string, { sha256: string; file: string }>;
export const shellQuote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'";

export function bootstrap(panelUrl: string, runId: string, token: string, binaries: Binaries, recover = false): string {
  for (const arch of ['amd64', 'arm64']) if (!/^[a-f0-9]{64}$/.test(binaries[arch]?.sha256)) throw new Error('执行器文件校验信息缺失，请重新安装完整插件包');
  const base = panelUrl + RUNNER_API;
  return `set -eu
umask 077
[ "$(uname -s)" = Linux ] || { echo 'Only Linux nodes are supported'; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) arch=amd64; expected=${shellQuote(binaries.amd64.sha256)} ;;
  aarch64|arm64) arch=arm64; expected=${shellQuote(binaries.arm64.sha256)} ;;
  *) echo 'Unsupported CPU architecture'; exit 1 ;;
esac
base="\${XDG_DATA_HOME:-\${HOME:?HOME is required}/.local/share}/komari-webdav-backup"
[ ! -L "$base" ] || { echo 'State directory must not be a symlink'; exit 1; }
mkdir -p "$base/bin" "$base/work" "$base/results"
chmod 700 "$base" "$base/bin" "$base/work" "$base/results"
runner="$base/bin/runner-$arch-$expected"
checksum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d ' ' -f 1
  else echo 'SHA256 tool is required' >&2; return 1; fi
}
if [ ! -f "$runner" ] || [ -L "$runner" ] || [ "$(checksum "$runner")" != "$expected" ]; then
  tmp=$(mktemp "$base/bin/download.XXXXXX")
  trap 'rm -f "$tmp"' EXIT HUP INT TERM
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --connect-timeout 15 --max-time 180 -H ${shellQuote('Authorization: Bearer ' + token)} ${shellQuote(base + '/binary?run=' + runId)}"&arch=$arch" -o "$tmp"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 180 --header=${shellQuote('Authorization: Bearer ' + token)} -O "$tmp" ${shellQuote(base + '/binary?run=' + runId)}"&arch=$arch"
  else echo 'curl or wget is required'; exit 1; fi
  [ "$(checksum "$tmp")" = "$expected" ] || { echo 'Runner SHA256 mismatch'; exit 1; }
  chmod 700 "$tmp"
  mv -f "$tmp" "$runner"
  trap - EXIT HUP INT TERM
fi
exec "$runner" ${recover ? 'status' : 'run'} --state-dir "$base" --run-id ${shellQuote(runId)}${recover ? '' : ' --claim-url ' + shellQuote(base + '/claim') + ' --grant ' + shellQuote(token)}
`;
}
