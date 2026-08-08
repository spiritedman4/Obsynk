import { App, Notice, PluginSettingTab, Setting } from "obsidian";

import type ObsynkPlugin from "../main";
import type { LoginEvent } from "../grpc/client";

export class ObsynkSettingTab extends PluginSettingTab {
	constructor(app: App, private plugin: ObsynkPlugin) {
		super(app, plugin);
	}

	display(): void {
		const { containerEl } = this;
		containerEl.empty();
		containerEl.addClass("obsynk-settings");
		containerEl.createEl("h2", { text: "Obsynk" });

		containerEl.createEl("p", {
			cls: "obsynk-hint",
			text: "Logs: obsynk.log (plugin) and obsynkd.log (sync daemon), both in this vault's .obsidian/plugins/obsynk/ folder.",
		});

		if (!this.plugin.daemon) {
			containerEl.createEl("p", {
				text: "The Obsynk daemon isn't running. Check obsynk.log and the developer console for errors, then reload the plugin.",
			});
			return;
		}
		const daemon = this.plugin.daemon;

		let clientId = "";
		let clientSecret = "";
		let rootFolderName = "";

		const statusEl = containerEl.createEl("p", { cls: "obsynk-status", text: "Loading status..." });
		const setStatusState = (state: "connected" | "disconnected" | "error") => {
			statusEl.removeClass("is-connected", "is-disconnected", "is-error");
			statusEl.addClass(`is-${state}`);
		};

		const folderEl = containerEl.createEl("p", { cls: "obsynk-folder-link" });
		folderEl.hide();
		const showFolderLink = (folderName: string, folderId: string) => {
			folderEl.empty();
			folderEl.appendText(`Synced to Drive folder "${folderName}" `);
			const link = folderEl.createEl("a", { text: "Open in Drive ↗", href: "#" });
			link.addEventListener("click", (evt) => {
				evt.preventDefault();
				// eslint-disable-next-line @typescript-eslint/no-var-requires
				require("electron").shell.openExternal(`https://drive.google.com/drive/folders/${folderId}`);
			});
			folderEl.show();
		};

		const refreshStatus = async () => {
			try {
				const [status, config] = await Promise.all([daemon.client.getStatus(), daemon.client.getConfig()]);
				clientId = config.client_id;
				rootFolderName = config.drive_root_folder_name;

				if (status.drive_connected) {
					const lastSync =
						status.last_sync_unix_ms === "0"
							? "never"
							: new Date(Number(status.last_sync_unix_ms)).toLocaleString();
					statusEl.setText(`Connected as ${status.drive_account_email}. Last synced: ${lastSync}.`);
					setStatusState("connected");

					if (config.drive_root_folder_id) {
						showFolderLink(config.drive_root_folder_name, config.drive_root_folder_id);
					} else {
						folderEl.hide();
					}
				} else {
					statusEl.setText("Not connected to Google Drive.");
					setStatusState("disconnected");
					folderEl.hide();
				}
			} catch (e) {
				statusEl.setText(`Daemon not reachable: ${(e as Error).message}`);
				setStatusState("error");
				folderEl.hide();
			}
		};
		void refreshStatus();

		new Setting(containerEl)
			.setName("Google OAuth Client ID")
			.setDesc("From your own Google Cloud project (OAuth client, application type: Desktop app).")
			.setClass("obsynk-wide-setting")
			.addText((text) => text.setValue(clientId).onChange((v) => (clientId = v)));

		new Setting(containerEl)
			.setName("Google OAuth Client Secret")
			.setClass("obsynk-wide-setting")
			.addText((text) => {
				text.inputEl.type = "password";
				text.onChange((v) => (clientSecret = v));
			});

		new Setting(containerEl)
			.setName("Drive root folder name")
			.setDesc('Defaults to "Obsidian Vault - <vault name>" if left blank.')
			.setClass("obsynk-wide-setting")
			.addText((text) => text.setValue(rootFolderName).onChange((v) => (rootFolderName = v)));

		new Setting(containerEl).setName("Connect to Google Drive").addButton((btn) =>
			btn
				.setButtonText("Connect")
				.setCta()
				.onClick(async () => {
					if (!clientId || !clientSecret) {
						new Notice("Enter both Client ID and Client Secret first.");
						return;
					}
					try {
						await daemon.client.setConfig({
							client_id: clientId,
							client_secret: clientSecret,
							drive_root_folder_name: rootFolderName,
						});
						await daemon.client.startLogin(clientId, clientSecret, (ev: LoginEvent) => {
							if ("authorize_url" in ev) {
								new Notice("Opening your browser to connect Google Drive...");
								// electron is external to the esbuild bundle; require it at
								// runtime the same way any Obsidian desktop plugin does.
								// eslint-disable-next-line @typescript-eslint/no-var-requires
								require("electron").shell.openExternal(ev.authorize_url);
							} else if ("connected" in ev) {
								new Notice("Connected to Google Drive!");
								void refreshStatus();
							} else if ("error" in ev) {
								new Notice(`Connection failed: ${ev.error}`);
							}
						});
					} catch (e) {
						new Notice(`Connection failed: ${(e as Error).message}`);
					}
				}),
		);

		const syncSetting = new Setting(containerEl)
			.setName("Sync now")
			.setDesc("Run a full reconciliation between this vault and Drive.");

		const progressEl = containerEl.createEl("p", { cls: "obsynk-sync-progress" });
		progressEl.hide();

		syncSetting.addButton((btn) =>
			btn.setButtonText("Sync now").onClick(async () => {
				btn.setDisabled(true);
				progressEl.show();
				progressEl.setText("Starting full sync...");
				try {
					await this.plugin.triggerFullSync((done, total, failures) => {
						if (total > 0) {
							const pct = Math.min(100, Math.round((done / total) * 100));
							progressEl.setText(
								`Syncing: ${done}/${total} operation(s) (${pct}%)${failures > 0 ? `, ${failures} failure(s)` : ""}`,
							);
						} else {
							progressEl.setText("Scanning vault and Drive...");
						}
					});
				} catch (e) {
					new Notice(`Full sync failed: ${(e as Error).message}`);
				} finally {
					btn.setDisabled(false);
					progressEl.hide();
					void refreshStatus();
				}
			}),
		);
	}
}
