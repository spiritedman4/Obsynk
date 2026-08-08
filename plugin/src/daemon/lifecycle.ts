import { ChildProcessByStdio, spawn } from "child_process";
import * as fs from "fs";
import * as path from "path";
import type { Readable } from "stream";

import { ObsynkClient } from "../grpc/client";
import { resolveDaemonBinaryPath } from "./binaries";
import { isProcessAlive, lockFilePath, readLockFile, removeLockFile, writeLockFile } from "./lockfile";

type ObsynkdProcess = ChildProcessByStdio<null, Readable, Readable>;

const OBSYNKD_ADDR_RE = /OBSYNKD_ADDR=127\.0\.0\.1:(\d+)/;

/** Minimal logging interface so this module doesn't depend on the concrete
 * PluginLogger -- callers that don't care can omit it and get console
 * output only (matches the prior hardcoded console.error behavior). */
export interface DaemonLogSink {
	info(message: string): void;
	error(message: string): void;
}

const consoleLogSink: DaemonLogSink = {
	info: (m) => console.log(m),
	error: (m) => console.error(m),
};

/** Owns the gRPC client and (if this plugin instance spawned it) the child
 * process for one vault's obsynkd daemon. */
export class DaemonHandle {
	constructor(
		public readonly client: ObsynkClient,
		private readonly child: ObsynkdProcess | null,
		private readonly lockPath: string,
	) {}

	/**
	 * If this instance spawned the daemon: sends the Shutdown RPC, waits
	 * briefly for the child to exit, kills it as a fallback, and removes
	 * the lockfile. If this instance only reused another window's daemon,
	 * it must NOT send Shutdown -- that daemon may still be serving that
	 * other window -- so it just closes its own gRPC connection.
	 */
	async shutdown(): Promise<void> {
		if (!this.child) {
			this.client.close();
			return;
		}

		try {
			await this.client.shutdown();
		} catch {
			// best-effort: daemon may already be gone
		}
		this.client.close();

		await new Promise<void>((resolve) => {
			const timeout = setTimeout(() => {
				try {
					this.child?.kill();
				} catch {
					// already dead
				}
				resolve();
			}, 3000);
			this.child!.once("exit", () => {
				clearTimeout(timeout);
				resolve();
			});
		});
		removeLockFile(this.lockPath);
	}
}

async function tryReuse(lockPath: string, pluginDir: string): Promise<DaemonHandle | null> {
	const info = readLockFile(lockPath);
	if (!info || !isProcessAlive(info.pid)) return null;

	const client = new ObsynkClient(`127.0.0.1:${info.port}`, pluginDir);
	try {
		await client.waitForReady(2000);
		await client.getStatus();
		return new DaemonHandle(client, null, lockPath);
	} catch {
		client.close();
		return null;
	}
}

function waitForDaemonPort(child: ObsynkdProcess, timeoutMs: number, log: DaemonLogSink): Promise<number> {
	return new Promise((resolve, reject) => {
		let buffer = "";
		const onData = (chunk: Buffer) => {
			buffer += chunk.toString();
			const match = OBSYNKD_ADDR_RE.exec(buffer);
			if (match) {
				child.stdout.off("data", onData);
				resolve(parseInt(match[1], 10));
			}
		};
		child.stdout.on("data", onData);
		// Persists for the child's whole lifetime (not removed after the
		// port is found), so daemon-side log lines keep flowing through.
		child.stderr.on("data", (chunk: Buffer) => log.info(`[obsynkd] ${chunk.toString().trimEnd()}`));
		child.once("error", (err) => reject(new Error(`spawning obsynkd failed: ${err.message}`)));
		child.once("exit", (code, signal) =>
			reject(new Error(`obsynkd exited before reporting its port (code=${code}, signal=${signal})`)),
		);
		const timer = setTimeout(() => reject(new Error("timed out waiting for obsynkd to report its port")), timeoutMs);
		child.once("exit", () => clearTimeout(timer));
	});
}

/**
 * Reuses an already-running daemon for this vault if its lockfile points at
 * a live, reachable process; otherwise spawns a fresh one (lifecycle tied
 * to this plugin instance -- non-detached, killed on unload).
 */
export async function startOrReuseDaemon(
	pluginDir: string,
	vaultPath: string,
	log: DaemonLogSink = consoleLogSink,
): Promise<DaemonHandle> {
	const dataDir = path.join(vaultPath, ".obsidian", "plugins", "obsynk");
	const lockPath = lockFilePath(dataDir);

	const reused = await tryReuse(lockPath, pluginDir);
	if (reused) {
		log.info(`Reusing already-running daemon (pid ${readLockFile(lockPath)?.pid}).`);
		return reused;
	}
	removeLockFile(lockPath);

	const binPath = resolveDaemonBinaryPath(pluginDir);
	log.info(`Starting obsynkd from ${binPath} for vault ${vaultPath}`);
	if (!fs.existsSync(binPath)) {
		throw new Error(
			`obsynkd binary not found at ${binPath}. Build it (see cmd/obsynkd) and copy it into plugin/bin/<platform>-<arch>/ as part of packaging.`,
		);
	}

	const child = spawn(binPath, ["-vault", vaultPath], {
		stdio: ["ignore", "pipe", "pipe"],
	}) as ObsynkdProcess;

	const port = await waitForDaemonPort(child, 10000, log);
	log.info(`obsynkd reported port ${port}, pid ${child.pid}`);

	const client = new ObsynkClient(`127.0.0.1:${port}`, pluginDir);
	await client.waitForReady(5000);

	writeLockFile(lockPath, { pid: child.pid!, port, startedAt: Date.now() });

	return new DaemonHandle(client, child, lockPath);
}
