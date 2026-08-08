import { App, Notice, PluginSettingTab, Setting, TextComponent } from "obsidian";

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

		// Held so the async config fetch below can populate them once it
		// returns -- the Setting rows are built synchronously, before the
		// daemon has answered, so seeding them with plain variables would
		// always render them empty even when credentials are saved.
		let clientIdInput: TextComponent | null = null;
		let secretInput: TextComponent | null = null;
		let folderInput: TextComponent | null = null;
		let hasSavedSecret = false;

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

				// Reflect saved config back into the inputs. The secret is
				// deliberately never sent back by the daemon (GetConfig only
				// reports whether one exists), so we show a placeholder
				// rather than putting a live credential in the DOM.
				clientIdInput?.setValue(config.client_id);
				folderInput?.setValue(config.drive_root_folder_name);
				hasSavedSecret = config.has_client_secret;
				if (secretInput) {
					secretInput.setPlaceholder(
						hasSavedSecret ? "Saved — leave blank to reuse" : "Paste your client secret",
					);
				}

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
			.addText((text) => {
				clientIdInput = text;
				text.setPlaceholder("xxxxx.apps.googleusercontent.com");
			});

		new Setting(containerEl)
			.setName("Google OAuth Client Secret")
			.setDesc("Stored locally in this vault's plugin folder; never uploaded to Drive.")
			.setClass("obsynk-wide-setting")
			.addText((text) => {
				secretInput = text;
				text.inputEl.type = "password";
			});

		new Setting(containerEl)
			.setName("Drive root folder name")
			.setDesc('Defaults to "Obsidian Vault - <vault name>" if left blank.')
			.setClass("obsynk-wide-setting")
			.addText((text) => {
				folderInput = text;
			});

		new Setting(containerEl)
			.setName("Connect to Google Drive")
			.setDesc(
				"Google expires test-user tokens after 7 days, so this needs re-running about weekly. " +
					"Your saved credentials are reused — you only need to re-enter them if they change.",
			)
			.addButton((btn) =>
				btn
					.setButtonText("Connect")
					.setCta()
					.onClick(async () => {
						const clientId = clientIdInput?.getValue().trim() ?? "";
						// Blank means "reuse the saved one": the daemon falls back to
						// its stored secret, and SetConfig ignores an empty value
						// rather than wiping what's there.
						const typedSecret = secretInput?.getValue() ?? "";
						const rootFolderName = folderInput?.getValue().trim() ?? "";

						if (!clientId) {
							new Notice("Enter your Google OAuth Client ID first.");
							return;
						}
						if (!typedSecret && !hasSavedSecret) {
							new Notice("Enter your Google OAuth Client Secret first.");
							return;
						}

						btn.setDisabled(true);
						try {
							await daemon.client.setConfig({
								client_id: clientId,
								client_secret: typedSecret,
								drive_root_folder_name: rootFolderName,
							});
							await daemon.client.startLogin(clientId, typedSecret, (ev: LoginEvent) => {
								if ("authorize_url" in ev) {
									new Notice("Opening your browser to connect Google Drive...");
									// electron is external to the esbuild bundle; require it at
									// runtime the same way any Obsidian desktop plugin does.
									// eslint-disable-next-line @typescript-eslint/no-var-requires
									require("electron").shell.openExternal(ev.authorize_url);
								} else if ("connected" in ev) {
									new Notice("Connected to Google Drive!");
									// Don't leave a live credential sitting in the DOM.
									secretInput?.setValue("");
									void refreshStatus();
								} else if ("error" in ev) {
									new Notice(`Connection failed: ${ev.error}`);
								}
							});
						} catch (e) {
							new Notice(`Connection failed: ${(e as Error).message}`);
						} finally {
							btn.setDisabled(false);
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
