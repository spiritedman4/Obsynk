import * as fs from "fs";
import * as path from "path";

const MAX_LOG_BYTES = 1_000_000;

/**
 * Writes timestamped lines to <pluginDataDir>/obsynk.log, in addition to
 * the console. A file survives across Obsidian restarts and doesn't
 * require the dev console to have been open at the time something went
 * wrong -- unlike console.log/error alone, which is what main.ts used
 * exclusively before this.
 */
export class PluginLogger {
	private readonly logPath: string;

	constructor(dataDir: string) {
		this.logPath = path.join(dataDir, "obsynk.log");
		try {
			fs.mkdirSync(dataDir, { recursive: true });
			const stat = fs.existsSync(this.logPath) ? fs.statSync(this.logPath) : null;
			if (stat && stat.size > MAX_LOG_BYTES) {
				fs.renameSync(this.logPath, `${this.logPath}.old`);
			}
		} catch {
			// Best-effort: a logging setup failure shouldn't break the plugin.
		}
	}

	info(message: string): void {
		this.write("INFO", message);
		console.log(`[Obsynk] ${message}`);
	}

	error(message: string): void {
		this.write("ERROR", message);
		console.error(`[Obsynk] ${message}`);
	}

	private write(level: string, message: string): void {
		const line = `${new Date().toISOString()} [${level}] ${message}\n`;
		try {
			fs.appendFileSync(this.logPath, line);
		} catch {
			// Best-effort: don't let a full disk etc. take down the plugin.
		}
	}
}
