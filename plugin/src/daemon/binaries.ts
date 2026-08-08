import * as path from "path";

/**
 * Resolves the path to the bundled obsynkd binary for the current
 * platform/arch. Personal-use scope currently only ships win32-x64; see
 * the plan's packaging section for what a multi-platform build would add.
 */
export function resolveDaemonBinaryPath(pluginDir: string): string {
	const platform = process.platform; // 'win32' | 'darwin' | 'linux'
	const arch = process.arch; // 'x64' | 'arm64'
	const exeName = platform === "win32" ? "obsynkd.exe" : "obsynkd";
	return path.join(pluginDir, "bin", `${platform}-${arch}`, exeName);
}
