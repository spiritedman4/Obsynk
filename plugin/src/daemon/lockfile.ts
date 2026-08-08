import * as fs from "fs";
import * as path from "path";

export interface LockInfo {
	pid: number;
	port: number;
	startedAt: number;
}

export function lockFilePath(pluginDataDir: string): string {
	return path.join(pluginDataDir, "daemon.lock");
}

export function readLockFile(lockPath: string): LockInfo | null {
	try {
		const raw = fs.readFileSync(lockPath, "utf-8");
		const parsed = JSON.parse(raw);
		if (typeof parsed.pid === "number" && typeof parsed.port === "number") {
			return parsed as LockInfo;
		}
		return null;
	} catch {
		return null;
	}
}

export function writeLockFile(lockPath: string, info: LockInfo): void {
	fs.mkdirSync(path.dirname(lockPath), { recursive: true });
	fs.writeFileSync(lockPath, JSON.stringify(info, null, 2), "utf-8");
}

export function removeLockFile(lockPath: string): void {
	try {
		fs.unlinkSync(lockPath);
	} catch {
		// already gone
	}
}

/** process.kill(pid, 0) throws if the process doesn't exist; it doesn't
 * actually send a signal with signal 0. */
export function isProcessAlive(pid: number): boolean {
	try {
		process.kill(pid, 0);
		return true;
	} catch {
		return false;
	}
}
