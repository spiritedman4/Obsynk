import type { Plugin } from "obsidian";

export class StatusBar {
	private el: HTMLElement;

	constructor(plugin: Plugin) {
		this.el = plugin.addStatusBarItem();
		this.setIdle();
	}

	setIdle(): void {
		this.el.setText("Obsynk: idle");
	}

	setSyncing(done: number, total: number): void {
		if (total > 0) {
			const pct = Math.min(100, Math.round((done / total) * 100));
			this.el.setText(`Obsynk: syncing ${done}/${total} (${pct}%)`);
		} else {
			this.el.setText("Obsynk: syncing...");
		}
	}

	setError(message: string): void {
		this.el.setText(`Obsynk: error - ${message}`);
	}

	setDisconnected(): void {
		this.el.setText("Obsynk: not connected to Drive");
	}
}
