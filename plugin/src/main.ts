import { FileSystemAdapter, Notice, Plugin, TAbstractFile } from "obsidian";
import * as path from "path";

import { DaemonHandle, startOrReuseDaemon } from "./daemon/lifecycle";
import { SyncEventQueue } from "./events/watcher";
import type { SyncProgressEvent } from "./grpc/client";
import { PluginLogger } from "./logging";
import { ObsynkSettingTab } from "./settings/settings-tab";
import { StatusBar } from "./ui/status-bar";

// Debounce window: absorbs editor autosave bursts and Obsidian's own
// internal multi-event churn (a single user save can fire more than one
// vault event for the same path) without needing to special-case them.
const SYNC_DEBOUNCE_MS = 1800;

export default class ObsynkPlugin extends Plugin {
	daemon: DaemonHandle | null = null;
	logger!: PluginLogger;
	private statusBar!: StatusBar;
	private eventQueue!: SyncEventQueue;

	async onload(): Promise<void> {
		this.statusBar = new StatusBar(this);
		this.addSettingTab(new ObsynkSettingTab(this.app, this));

		this.eventQueue = new SyncEventQueue(SYNC_DEBOUNCE_MS, (events) => {
			void this.runSync(
				`vault event batch (${events.map((e) => e.relative_path).join(", ")})`,
				(onProgress) => this.daemon!.client.syncPaths(events, onProgress),
			);
		});

		this.addCommand({
			id: "obsynk-sync-now",
			name: "Sync now",
			callback: () => this.syncNow(),
		});
		this.addRibbonIcon("refresh-cw", "Obsynk: Sync now", () => this.syncNow());

		const adapter = this.app.vault.adapter;
		if (!(adapter instanceof FileSystemAdapter)) {
			console.error("Obsynk requires a local filesystem vault (desktop only).");
			this.statusBar.setError("desktop only");
			return;
		}
		const vaultPath = adapter.getBasePath();
		const pluginDir = path.join(vaultPath, ".obsidian", "plugins", this.manifest.id);
		const dataDir = path.join(pluginDir); // plugin install dir doubles as its data dir
		this.logger = new PluginLogger(dataDir);
		this.logger.info(`Obsynk loading. vault=${vaultPath} pluginDir=${pluginDir}`);

		try {
			this.daemon = await startOrReuseDaemon(pluginDir, vaultPath, this.logger);
			this.logger.info("Daemon ready.");
			await this.refreshConnectionStatus();
		} catch (e) {
			const message = (e as Error).message;
			this.logger.error(`Failed to start daemon: ${message}`);
			new Notice(`Obsynk: failed to start sync daemon (${message}). See obsynk.log in the plugin folder for details.`);
			this.statusBar.setError("daemon failed to start");
			return;
		}

		// Registered only once the workspace is ready. Obsidian replays a
		// "create" for every existing file while it builds its initial index,
		// so subscribing during onload enqueues the entire vault as a single
		// event batch -- a full sync in all but name, which would then run
		// concurrently with any real sync and duplicate every folder on Drive.
		this.app.workspace.onLayoutReady(() => {
			this.registerEvent(this.app.vault.on("modify", (f: TAbstractFile) => this.eventQueue.enqueue(f.path, "MODIFY")));
			this.registerEvent(this.app.vault.on("create", (f: TAbstractFile) => this.eventQueue.enqueue(f.path, "CREATE")));
			this.registerEvent(this.app.vault.on("delete", (f: TAbstractFile) => this.eventQueue.enqueue(f.path, "DELETE")));
			this.registerEvent(
				this.app.vault.on("rename", (f: TAbstractFile, oldPath: string) =>
					this.eventQueue.enqueue(f.path, "RENAME", oldPath),
				),
			);
			this.logger?.info("Vault watchers registered (post-layout).");
		});
	}

	async onunload(): Promise<void> {
		this.eventQueue?.cancel();
		if (this.daemon) {
			this.logger?.info("Plugin unloading, shutting down daemon connection.");
			await this.daemon.shutdown();
			this.daemon = null;
		}
	}

	private async refreshConnectionStatus(): Promise<void> {
		if (!this.daemon) return;
		try {
			const status = await this.daemon.client.getStatus();
			if (status.drive_connected) {
				this.logger.info(`Drive connected as ${status.drive_account_email}.`);
				this.statusBar.setIdle();
			} else {
				this.logger.info("Drive not connected yet.");
				this.statusBar.setDisconnected();
			}
		} catch (e) {
			this.logger.error(`GetStatus failed: ${(e as Error).message}`);
			this.statusBar.setError("unreachable");
		}
	}

	private syncNow(): void {
		if (!this.daemon) {
			new Notice("Obsynk daemon is not running.");
			return;
		}
		new Notice("Obsynk: starting full sync...");
		void this.triggerFullSync();
	}

	/**
	 * Runs a manual FullSync. Public so the settings tab's "Sync now"
	 * button can trigger the same instrumented path (status bar + logging
	 * + Notices) as the command palette/ribbon icon, while also getting its
	 * own live progress callback for an in-tab progress display.
	 */
	async triggerFullSync(onUpdate?: (done: number, total: number, failures: number) => void): Promise<void> {
		await this.runSync("manual Sync now", (onProgress) => this.daemon!.client.fullSync(onProgress), onUpdate);
	}

	/**
	 * Runs a sync (either a debounced SyncPaths batch or a manual
	 * FullSync), tracking live progress (with a real total/percentage, now
	 * that the daemon reports total_operations on every message) in the
	 * status bar and via the optional onUpdate callback, logging a summary
	 * line for every attempt (success or failure) to obsynk.log, and
	 * surfacing failures/conflicts via Notice -- deliberately not spamming
	 * a Notice per successful file, per the plan's UI design.
	 */
	private async runSync(
		trigger: string,
		start: (onProgress: (p: SyncProgressEvent) => void) => Promise<void>,
		onUpdate?: (done: number, total: number, failures: number) => void,
	): Promise<void> {
		if (!this.daemon) return;

		const startedAt = Date.now();
		let done = 0;
		let total = 0;
		let failures = 0;
		let conflicts = 0;

		this.statusBar.setSyncing(0, 0);
		onUpdate?.(0, 0, 0);
		const onProgress = (p: SyncProgressEvent) => {
			done++;
			total = p.total_operations;
			if (p.status === "FAILED") {
				failures++;
				this.logger.error(`${p.relative_path} (${p.op}) failed: ${p.error_message}`);
			} else {
				this.logger.info(`${p.relative_path}: ${p.op} ${p.status}`);
			}
			if (p.op === "CONFLICT_RESOLVED") conflicts++;
			this.statusBar.setSyncing(done, total);
			onUpdate?.(done, total, failures);
		};

		try {
			await start(onProgress);
		} catch (e) {
			const message = (e as Error).message;
			this.logger.error(`Sync failed (${trigger}) after ${done} operation(s), ${Date.now() - startedAt}ms: ${message}`);
			this.statusBar.setError(message);
			new Notice(`Obsynk sync failed: ${message}`);
			return;
		}

		const durationMs = Date.now() - startedAt;
		this.logger.info(
			`Sync complete (${trigger}): ${done} operation(s), ${failures} failure(s), ${conflicts} conflict(s), ${durationMs}ms.`,
		);

		if (failures > 0) {
			new Notice(`Obsynk: sync finished with ${failures} failure(s) out of ${done} operation(s).`);
			this.statusBar.setError(`${failures} failure(s)`);
		} else {
			this.statusBar.setIdle();
		}
		if (conflicts > 0) {
			// Known simplification: the daemon streams that a conflict was
			// resolved (CONFLICT_RESOLVED) but not which side won -- adding
			// that would mean extending the proto's SyncProgress message.
			// The per-path log lines above at least record which paths hit
			// it, even without the winner.
			new Notice(`Obsynk: resolved ${conflicts} conflict(s) via last-write-wins (see obsynk.log for paths).`);
		}
	}
}
